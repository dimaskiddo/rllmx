package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"

	"rllmx/app/store/dialect"
	"rllmx/app/store/sealer"
	"rllmx/tests"
)

const (
	legacyKey  = "sk-legacy-key-aaaaaa"
	legacyConn = "sk-provider-legacy"
	legacyOIDC = "idp-legacy-secret"
)

func TestMigrateCreatesAFreshDatabase(t *testing.T) {
	s := newMigrated(t)
	for _, tb := range tables {
		if !s.hasTable(tb.Name) {
			t.Errorf("table %s missing", tb.Name)
		}
		for _, ix := range tb.Indexes {
			if !s.hasIndex(t, tb.Name, ix.Name) {
				t.Errorf("index %s missing", ix.Name)
			}
		}
	}
	for _, usage := range []string{"usageHistory", "usageDaily", "requestDetails"} {
		if s.hasTable(usage) {
			t.Errorf("a fresh database must not have %s", usage)
		}
	}
	meta := map[string]string{}
	for _, k := range []string{"schemaVersion", "backupSchemaVersion", "appVersion", "migratedAt"} {
		meta[k] = s.one(t, `SELECT "value" FROM "_meta" WHERE "key"=?`, k)
	}
	if meta["schemaVersion"] != "1" || meta["backupSchemaVersion"] != "2" || meta["migratedAt"] == "" {
		t.Fatalf("_meta = %v", meta)
	}
	if meta["appVersion"] != "rllmx-test" {
		t.Fatalf("appVersion = %q", meta["appVersion"])
	}
	if got := s.one(t, `SELECT count(*) FROM "kv" WHERE "scope"='rllmCrypto' AND "key"='check'`); got != "1" {
		t.Fatal("a new database gets no check row")
	}
}

func TestMigrateOfAFreshDatabaseMakesNoBackup(t *testing.T) {
	s := newMigrated(t)
	if n := backupNames(t, s.SQ.File); len(n) != 0 {
		t.Fatalf("a new database was backed up: %v", n)
	}
}

func TestMigrateTwiceWritesNothing(t *testing.T) {
	s := newMigrated(t)
	file := s.SQ.File
	s.SQ.Close()
	before := dumpTables(t, file)

	s2 := reopen(t, file, tests.DBSecret)
	warns, err := s2.Migrate(context.Background())
	if err != nil || len(warns) != 0 {
		t.Fatalf("Migrate = %v, %v", warns, err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("second Migrate changed %v", diff)
	}
	if n := backupNames(t, file); len(n) != 0 {
		t.Fatalf("a backup appeared although nothing changed: %v", n)
	}
}

func TestMissingColumnIsAddedAfterABackup(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `ALTER TABLE "apiKeys" DROP COLUMN "accessAllow"`)
	s.exec(t, `INSERT INTO "apiKeys"("id","key","name","machineId","createdAt") VALUES ('k1','n-key','n','m','2026-01-01T00:00:00.000Z')`)
	s.exec(t, `DELETE FROM "apiKeys"`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s2.one(t, `SELECT count(*) FROM pragma_table_info('apiKeys') WHERE name='accessAllow'`); got != "1" {
		t.Fatal("column not added")
	}
	names := backupNames(t, file)
	if len(names) != 1 || !strings.HasPrefix(names[0], "schema-change-") {
		t.Fatalf("backups = %v, want one schema-change backup", names)
	}
	bk := filepath.Join(filepath.Dir(file), "backups", names[0], "data.sqlite")
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(bk); st.Mode().Perm() != 0o600 {
			t.Errorf("backup file mode = %v, want 0600", st.Mode().Perm())
		}
		if st, _ := os.Stat(filepath.Dir(bk)); st.Mode().Perm() != 0o700 {
			t.Errorf("backup directory mode = %v, want 0700", st.Mode().Perm())
		}
	}
	ro := tests.OpenSQLiteReadOnly(t, bk)
	var cols []string
	if err := ro.DB.Select(&cols, `SELECT name FROM pragma_table_info('apiKeys') WHERE name='accessAllow'`); err != nil || len(cols) != 0 {
		t.Fatalf("the backup must hold the database as it was before the change: %v %v", cols, err)
	}
}

func TestADroppedIndexIsRestoredEvenAfterTheMigratorRan(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `DROP INDEX "idx_pc_provider"`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s2.one(t, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_pc_provider'`); got != "1" {
		t.Fatal("the index was not restored")
	}
	again := reopen(t, file, tests.DBSecret)
	before := len(backupNames(t, file))
	if _, err := again.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(backupNames(t, file)) != before {
		t.Fatal("a repaired database is backed up again on every start")
	}
}

func TestAMissingColumnThatCannotBeAddedIsAnError(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `ALTER TABLE "providerNodes" DROP COLUMN "data"`)
	file := s.SQ.File
	s.SQ.Close()
	before := dumpTables(t, file)

	s2 := reopen(t, file, tests.DBSecret)
	_, err := s2.Migrate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "providerNodes") {
		t.Fatalf("err = %v", err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("a refused migration changed %v", diff)
	}
	if n := backupNames(t, file); len(n) != 0 {
		t.Fatalf("a refused migration made a backup: %v", n)
	}
}

func TestADatabaseWithoutMetaButWithTablesIsBackedUpBeforeItChanges(t *testing.T) {
	for name, strip := range map[string]string{"missing": `DROP TABLE "_meta"`, "empty": `DELETE FROM "_meta"`} {
		t.Run(name, func(t *testing.T) {
			s := newMigrated(t)
			s.exec(t, `INSERT INTO "combos"("id","name","models","createdAt","updatedAt") VALUES ('c1','keep','[]','t','t')`)
			s.exec(t, strip)
			file := s.SQ.File
			s.SQ.Close()

			s2 := reopen(t, file, tests.DBSecret)
			if _, err := s2.Migrate(context.Background()); err != nil {
				t.Fatal(err)
			}
			if n := backupNames(t, file); len(n) != 1 {
				t.Fatalf("a database with data and no _meta rows must be backed up first: %v", n)
			}
			if got := s2.one(t, `SELECT count(*) FROM "_meta" WHERE "key" = 'schemaVersion'`); got != "1" {
				t.Fatalf("_meta was not seeded: %s", got)
			}
			if got := s2.one(t, `SELECT count(*) FROM "combos"`); got != "1" {
				t.Fatalf("row lost: %s", got)
			}
		})
	}
}

func TestFailedBackupAbortsTheChange(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `ALTER TABLE "apiKeys" DROP COLUMN "accessAllow"`)
	file := s.SQ.File
	s.SQ.Close()
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "backups"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	before := dumpTables(t, file)

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate succeeded although the backup could not be written")
	}
	if got := s2.one(t, `SELECT count(*) FROM pragma_table_info('apiKeys') WHERE name='accessAllow'`); got != "0" {
		t.Fatal("the schema was changed although the backup failed")
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("an aborted change wrote %v", diff)
	}
}

func TestNewerBackupSchemaVersionWarnsAndOpens(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `UPDATE "_meta" SET "value"='9' WHERE "key"='backupSchemaVersion'`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	warns, err := s2.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) == 0 || !strings.Contains(strings.Join(warns, " "), "newer") {
		t.Fatalf("no warning about a newer database: %v", warns)
	}
	if v := s2.one(t, `SELECT "value" FROM "_meta" WHERE "key"='backupSchemaVersion'`); v != "9" {
		t.Fatalf("backupSchemaVersion was rewritten to %q", v)
	}
}

func TestExistingMetaIsNeverRewritten(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `UPDATE "_meta" SET "value"='0.5.104-extended' WHERE "key"='appVersion'`)
	s.exec(t, `INSERT INTO "_meta"("key","value") VALUES ('totalRequestsLifetime','95099')`)
	s.exec(t, `ALTER TABLE "apiKeys" DROP COLUMN "accessAllow"`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"appVersion": "0.5.104-extended", "totalRequestsLifetime": "95099", "schemaVersion": "1", "backupSchemaVersion": "2"} {
		if v := s2.one(t, `SELECT "value" FROM "_meta" WHERE "key"=?`, k); v != want {
			t.Errorf("_meta %s = %q, want %q", k, v, want)
		}
	}
}

func TestUnknownObjectsSurvive(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `CREATE TABLE "proxyPoolFitness" ("poolId" TEXT NOT NULL, "until" INTEGER NOT NULL, PRIMARY KEY ("poolId"))`)
	s.exec(t, `INSERT INTO "proxyPoolFitness" VALUES ('p', 123)`)
	s.exec(t, `ALTER TABLE "apiKeys" DROP COLUMN "accessAllow"`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s2.one(t, `SELECT "until" FROM "proxyPoolFitness"`); got != "123" {
		t.Fatalf("unknown table damaged: %s", got)
	}
}

func TestNinerouterStyleDatabaseIsOpenedWithoutAMigrationsTable(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `DROP TABLE "migrations"`)
	file := s.SQ.File
	s.SQ.Close()
	before := dumpTables(t, file)

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("a current database was written: %v", diff)
	}
	if got := s2.one(t, `SELECT count(*) FROM sqlite_master WHERE name='migrations'`); got != "0" {
		t.Fatal("an up-to-date database got the framework's migrations table")
	}
}

func TestMigrateWithoutAMigratorFailsWhenSomethingIsMissing(t *testing.T) {
	sq := tests.NewSQLite(t)
	s, err := New(Options{DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Box: tests.Box(t, tests.DBSecret), Dialect: dialect.SQLite})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate without a migrator created a database")
	}
}

func makeLegacy(t *testing.T) string {
	t.Helper()
	s := newMigrated(t)
	s.exec(t, `INSERT INTO "apiKeys"("id","key","name","machineId","createdAt") VALUES ('k1',?,'ci','m1','2026-01-01T00:00:00.000Z')`, legacyKey)
	s.exec(t, `INSERT INTO "providerConnections"("id","provider","authType","name","data","createdAt","updatedAt") VALUES ('c1','openai','apikey','main',?,'t','t')`,
		`{"apiKey":"`+legacyConn+`","refreshToken":"rt-legacy","displayName":"Main","providerSpecificData":{"secretAccessKey":"aws-legacy","region":"eu"}}`)
	s.exec(t, `INSERT INTO "settings"("id","data") VALUES (1,?)`, `{"oidcClientSecret":"`+legacyOIDC+`","cavemanEnabled":true}`)
	s.exec(t, `DELETE FROM "kv" WHERE "scope"='rllmCrypto'`)
	file := s.SQ.File
	s.SQ.Close()
	return file
}

func TestPlainDatabaseIsSealedAfterABackup(t *testing.T) {
	file := makeLegacy(t)
	before := dumpTables(t, file)

	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	names := backupNames(t, file)
	if len(names) != 1 || !strings.HasPrefix(names[0], preEncryptionLabel+"-") {
		t.Fatalf("backups = %v", names)
	}
	bf := filepath.Join(filepath.Dir(file), "backups", names[0], "data.sqlite")
	if diff := diffTables(before, dumpTables(t, bf)); len(diff) != 0 {
		t.Fatalf("backup differs from the original: %v", diff)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(bf); fi.Mode().Perm() != 0o600 {
			t.Fatalf("backup mode = %v", fi.Mode().Perm())
		}
	}
	if k := s.one(t, `SELECT "key" FROM "apiKeys"`); !sealer.IsSealed(k) {
		t.Fatalf("key column = %q", k)
	}
	conn := s.one(t, `SELECT "data" FROM "providerConnections"`)
	for _, plain := range []string{legacyConn, "rt-legacy", "aws-legacy"} {
		if strings.Contains(conn, plain) {
			t.Fatal("connection data still holds a plain secret")
		}
	}
	if !strings.Contains(conn, `"displayName":"Main"`) || !strings.Contains(conn, `"region":"eu"`) {
		t.Fatalf("non-secret fields changed: %s", conn)
	}
	if set := s.one(t, `SELECT "data" FROM "settings"`); strings.Contains(set, legacyOIDC) || !strings.Contains(set, `"cavemanEnabled":true`) {
		t.Fatalf("settings = %s", set)
	}
	if got, err := s.OpenAPIKey(s.one(t, `SELECT "key" FROM "apiKeys"`)); err != nil || got != legacyKey {
		t.Fatalf("OpenAPIKey = %q, %v", got, err)
	}
	want, err := s.SealAPIKey(legacyKey)
	if err != nil {
		t.Fatal(err)
	}
	if s.one(t, `SELECT "key" FROM "apiKeys"`) != want {
		t.Fatal("the stored key is not the exact-match form a lookup compares with")
	}
}

func TestSecondOpenWritesNothingAndMakesNoBackup(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.SQ.Close()
	before := dumpTables(t, file)
	n := len(backupNames(t, file))

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("second open changed %v", diff)
	}
	if len(backupNames(t, file)) != n {
		t.Fatal("second open made a backup")
	}
}

func TestWrongSecretIsRefusedAndNothingChanges(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.SQ.Close()
	before := dumpTables(t, file)
	n := len(backupNames(t, file))

	wrong := reopen(t, file, tests.OtherDBSecret)
	if _, err := wrong.Migrate(context.Background()); !errors.Is(err, ErrWrongEncryptionSecret) {
		t.Fatalf("Migrate err = %v", err)
	}
	if err := wrong.Verify(context.Background()); !errors.Is(err, ErrWrongEncryptionSecret) {
		t.Fatalf("Verify err = %v", err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("a refused open changed %v", diff)
	}
	if len(backupNames(t, file)) != n {
		t.Fatal("a refused open made a backup")
	}
}

func TestWrongSecretIsRefusedWhenTheCheckRowIsMissing(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.exec(t, `DELETE FROM "kv" WHERE "scope"='rllmCrypto'`)
	s.SQ.Close()
	before := dumpTables(t, file)
	n := len(backupNames(t, file))

	wrong := reopen(t, file, tests.OtherDBSecret)
	if _, err := wrong.Migrate(context.Background()); !errors.Is(err, ErrWrongEncryptionSecret) {
		t.Fatalf("Migrate err = %v", err)
	}
	if err := wrong.Verify(context.Background()); !errors.Is(err, ErrWrongEncryptionSecret) {
		t.Fatalf("Verify err = %v", err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 || len(backupNames(t, file)) != n {
		t.Fatalf("a refused open changed %v", diff)
	}
}

func TestMissingSecretIsRefused(t *testing.T) {
	file := makeLegacy(t)
	before := dumpTables(t, file)
	s := reopen(t, file, "")
	if _, err := s.Migrate(context.Background()); !errors.Is(err, ErrEncryptionSecret) {
		t.Fatalf("Migrate err = %v", err)
	}
	if err := s.Verify(context.Background()); !errors.Is(err, ErrEncryptionSecret) {
		t.Fatalf("Verify err = %v", err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 || len(backupNames(t, file)) != 0 {
		t.Fatalf("a refused open changed %v", diff)
	}
}

func TestFailedBackupAbortsTheConversion(t *testing.T) {
	file := makeLegacy(t)
	before := dumpTables(t, file)
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), "backups"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err == nil {
		t.Fatal("Migrate succeeded without a backup")
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("an aborted conversion changed %v", diff)
	}
}

func TestVerifyNeverConverts(t *testing.T) {
	file := makeLegacy(t)
	before := dumpTables(t, file)
	s := reopen(t, file, tests.DBSecret)
	if err := s.Verify(context.Background()); !errors.Is(err, ErrNeedsMigration) {
		t.Fatalf("Verify err = %v", err)
	}
	if diff := diffTables(before, dumpTables(t, file)); len(diff) != 0 {
		t.Fatalf("Verify changed %v", diff)
	}
}

func TestVerifyRefusesADatabaseThatNeedsASchemaChange(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `DROP TABLE "combos"`)
	if err := s.Verify(context.Background()); !errors.Is(err, ErrNeedsMigration) {
		t.Fatalf("Verify err = %v", err)
	}
}

func TestVerifyAcceptsAMigratedDatabaseWithExtraTables(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `CREATE TABLE "extra"(a TEXT)`)
	if err := s.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCorruptRowStaysUntouched(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	const bad = `{"apiKey":"sk-x" broken`
	s.exec(t, `INSERT INTO "providerConnections"("id","provider","authType","data","createdAt","updatedAt") VALUES ('bad','openai','apikey',?,'t','t')`, bad)

	warns, err := s.Migrate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.one(t, `SELECT "data" FROM "providerConnections" WHERE "id"='bad'`); got != bad {
		t.Fatal("a damaged row was rewritten")
	}
	if !strings.Contains(strings.Join(warns, " "), "damaged JSON") {
		t.Fatalf("no warning for the damaged row: %v", warns)
	}
}

func TestDatabaseWithoutSecretsStillGetsTheCheckRow(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `DELETE FROM "kv" WHERE "scope"='rllmCrypto'`)
	file := s.SQ.File
	s.SQ.Close()

	s2 := reopen(t, file, tests.DBSecret)
	if _, err := s2.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := s2.one(t, `SELECT count(*) FROM "kv" WHERE "scope"='rllmCrypto' AND "key"='check'`); got != "1" {
		t.Fatal("the check row was not written")
	}
	if n := backupNames(t, file); len(n) != 0 {
		t.Fatalf("a check-only write made %d backups", len(n))
	}
}

func TestOnlyTheThreeNewestSchemaChangeBackupsAreKept(t *testing.T) {
	bdir := filepath.Join(t.TempDir(), "backups")
	if err := os.MkdirAll(bdir, 0o700); err != nil {
		t.Fatal(err)
	}
	set := func(name, day string) {
		p := filepath.Join(bdir, name)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		tm, _ := dialect.ParseTimestamp(day + "T00:00:00.000Z")
		if err := os.Chtimes(p, tm, tm); err != nil {
			t.Fatal(err)
		}
	}
	for i, n := range []string{"schema-change-a", "schema-change-b", "schema-change-c", "schema-change-d", "schema-change-e"} {
		set(n, "2026-01-0"+string(rune('1'+i)))
	}
	set("manual-old", "2025-01-01")
	set("pre-encryption-old", "2025-01-01")
	if err := pruneBackups(bdir, 3); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(bdir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := "manual-old,pre-encryption-old,schema-change-c,schema-change-d,schema-change-e"
	if strings.Join(names, ",") != want {
		t.Fatalf("kept %v, want %s", names, want)
	}
}

func TestConversionBackupIsNotAScheduleChangeBackup(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(filepath.Dir(file), "backups")
	for range 5 {
		if _, err := s.backup(context.Background(), root, "schema-change"); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(root, keptBackups); err != nil {
		t.Fatal(err)
	}
	var pre, sc int
	for _, n := range backupNames(t, file) {
		switch {
		case strings.HasPrefix(n, preEncryptionLabel+"-"):
			pre++
		case strings.HasPrefix(n, schemaChangePrefix):
			sc++
		}
	}
	if pre != 1 || sc != keptBackups {
		t.Fatalf("pre=%d schema=%d", pre, sc)
	}
}

func TestSeedingMetaTwiceIsHarmless(t *testing.T) {
	s := newMigrated(t)
	err := s.Tx(context.Background(), func(tx contractsdb.Tx) error { return s.seedMeta(tx) })
	if err != nil {
		t.Fatalf("a second process seeding the same database must not fail: %v", err)
	}
	if got := s.one(t, `SELECT count(*) FROM "_meta" WHERE "key" = 'schemaVersion'`); got != "1" {
		t.Fatalf("schemaVersion rows: %s", got)
	}
}

func TestSeedingMetaNeverOverwritesAValue(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `UPDATE "_meta" SET "value"='0.5.104-extended' WHERE "key"='appVersion'`)
	s.exec(t, `UPDATE "_meta" SET "value"='9' WHERE "key"='schemaVersion'`)
	if err := s.Tx(context.Background(), func(tx contractsdb.Tx) error { return s.seedMeta(tx) }); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"appVersion": "0.5.104-extended", "schemaVersion": "9"} {
		if got := s.one(t, `SELECT "value" FROM "_meta" WHERE "key"=?`, k); got != want {
			t.Errorf("_meta %s = %q, want %q", k, got, want)
		}
	}
}

func TestSealingLeavesNoPlainSecretInTheFileOrItsWAL(t *testing.T) {
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{file, file + "-wal"} {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		for _, plain := range []string{legacyKey, legacyConn, "rt-legacy", "aws-legacy", legacyOIDC} {
			if strings.Contains(string(b), plain) {
				t.Errorf("%s still holds %q after sealing: the rebuild and checkpoint did not run", filepath.Base(name), plain)
			}
		}
	}
}

type recordingSchema struct {
	contractsschema.Schema
	asked []string
}

func (r *recordingSchema) HasTable(name string) bool {
	r.asked = append(r.asked, name)
	return true
}

func TestPostgresTablesAreAskedBySchemaQualifiedName(t *testing.T) {
	rec := &recordingSchema{}
	s := &Store{schema: rec, dialect: dialect.Postgres, pgSchema: "rllmx"}
	s.hasTable("kv")
	if len(rec.asked) != 1 || rec.asked[0] != "rllmx.kv" {
		t.Fatalf("asked %v: HasTable looks in every schema unless the name is qualified", rec.asked)
	}
	rec = &recordingSchema{}
	lite := &Store{schema: rec, dialect: dialect.SQLite, pgSchema: "ignored"}
	lite.hasTable("kv")
	if len(rec.asked) != 1 || rec.asked[0] != "kv" {
		t.Fatalf("asked %v", rec.asked)
	}
}

// A connection deleted before the move leaves its plain credentials in free
// pages of the file. Sealing the remaining rows does not touch those pages, so
// the rebuild after sealing is what removes them. The connection here has
// secure_delete off, the case the rebuild exists for.
func TestRebuildRemovesPlainSecretsLeftInFreePages(t *testing.T) {
	const off = "_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=secure_delete(OFF)"
	file := filepath.Join(t.TempDir(), "data.sqlite")
	sq0 := tests.OpenSQLiteWithQuery(t, file, off)
	s0 := &testStore{Store: storeOver(t, sq0, tests.Box(t, tests.DBSecret)), SQ: sq0}
	if _, err := s0.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	pad := strings.Repeat("p", 9000)
	for i := range 6 {
		s0.exec(t, `INSERT INTO "providerConnections"("id","provider","authType","name","data","createdAt","updatedAt") VALUES (?,'openai','apikey','n',?,'t','t')`,
			"gone"+string(rune('a'+i)), `{"displayName":"`+pad+`","apiKey":"sk-deleted-long-ago"}`)
	}
	s0.exec(t, `DELETE FROM "providerConnections"`)
	s0.exec(t, `INSERT INTO "providerConnections"("id","provider","authType","name","data","createdAt","updatedAt") VALUES ('c1','openai','apikey','main',?,'t','t')`, `{"apiKey":"sk-still-here"}`)
	s0.exec(t, `DELETE FROM "kv" WHERE "scope"='rllmCrypto'`)
	if err := s0.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	s0.SQ.Close()
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "sk-deleted-long-ago") {
		t.Skip("the free pages did not keep the old secret, so there is nothing for the rebuild to remove")
	}

	sq := tests.OpenSQLiteWithQuery(t, file, off)
	s := &testStore{Store: storeOver(t, sq, tests.Box(t, tests.DBSecret))}
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	sq.Close()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"sk-deleted-long-ago", "sk-still-here"} {
		if strings.Contains(string(b), plain) {
			t.Errorf("the database file still holds %q after sealing", plain)
		}
	}
}

func TestDiffTablesReportsATableThatAppearedOnOneSide(t *testing.T) {
	before := map[string][]string{"kv": {"a"}}
	after := map[string][]string{"kv": {"a"}, "rllmNew": {"b"}}
	if d := diffTables(before, after); len(d) != 1 {
		t.Fatalf("a table that only appeared was not reported: %v", d)
	}
	if d := diffTables(after, before); len(d) != 1 {
		t.Fatalf("a table that only disappeared was not reported: %v", d)
	}
	if d := diffTables(before, before); len(d) != 0 {
		t.Fatalf("an identical dump differs: %v", d)
	}
}

func TestDumpTablesCarriesTheDDL(t *testing.T) {
	s := newMigrated(t)
	before := dumpTables(t, s.SQ.File)
	if len(before["<schema>"]) == 0 {
		t.Fatal("the dump carries no DDL, so a change to a table definition would go unseen")
	}
	s.exec(t, `CREATE TABLE "probe" ("id" TEXT PRIMARY KEY)`)
	after := dumpTables(t, s.SQ.File)
	if d := diffTables(before, after); len(d) == 0 {
		t.Fatal("a table that was created was not reported")
	}
}

// A database opened before the user tables existed has everything else, so its
// plan must still ask for them: the server logged "no such table" on every key
// list because the plan once covered only the compatibility set.
func TestADatabaseWithoutTheUserTablesGainsThem(t *testing.T) {
	for name, strip := range map[string][]string{
		"never created":     {`DROP TABLE "userKeys"`, `DROP TABLE "users"`, `DELETE FROM "migrations" WHERE "migration" LIKE '%users_table' OR "migration" LIKE '%userKeys_table'`},
		"dropped afterward": {`DROP TABLE "userKeys"`, `DROP TABLE "users"`},
	} {
		t.Run(name, func(t *testing.T) {
			s := newMigrated(t)
			for _, stmt := range strip {
				s.exec(t, stmt)
			}
			file := s.SQ.File
			s.SQ.Close()

			s2 := reopen(t, file, tests.DBSecret)
			ctx := context.Background()
			if need, err := s2.NeedsMigration(ctx); err != nil || !need {
				t.Fatalf("NeedsMigration = %v, %v", need, err)
			}
			if err := s2.Verify(ctx); !errors.Is(err, ErrNeedsMigration) {
				t.Fatalf("Verify = %v", err)
			}
			if _, err := s2.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			for _, tb := range ownTables {
				if !s2.hasTable(tb.Name) {
					t.Errorf("%s is still missing", tb.Name)
				}
				for _, ix := range tb.Indexes {
					if !s2.hasIndex(t, tb.Name, ix.Name) {
						t.Errorf("index %s is still missing", ix.Name)
					}
				}
			}
			if _, err := s2.Users().Owners(ctx); err != nil {
				t.Fatalf("the user tables do not answer: %v", err)
			}
		})
	}
}
