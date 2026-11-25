package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rllmx/app/store/dialect"
	"rllmx/tests"
)

func TestBackupWritesAnOwnerOnlyCopyThatOpens(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	s.exec(t, `INSERT INTO "settings"("id","data") VALUES (1,'{"marker":"present"}')`)
	out := t.TempDir()
	file, err := s.Backup(context.Background(), out, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("backup file: %v %v", info, err)
		}
		if info, _ := os.Stat(filepath.Dir(file)); info.Mode().Perm() != 0o700 {
			t.Fatalf("backup dir mode %v", info.Mode().Perm())
		}
	}
	if !strings.HasPrefix(filepath.Base(filepath.Dir(file)), "manual-rllmx-test-") {
		t.Fatalf("backup dir name %q", filepath.Dir(file))
	}
	ro := tests.OpenSQLiteReadOnly(t, file)
	var data []string
	if err := ro.DB.Select(&data, `SELECT data FROM settings WHERE id = 1`); err != nil || len(data) != 1 || !strings.Contains(data[0], "present") {
		t.Fatalf("backup content: %v %v", data, err)
	}
}

func TestBackupsOfTheSameLabelInTheSameSecondDoNotCollide(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	out := t.TempDir()
	seen := map[string]bool{}
	for range 5 {
		file, err := s.Backup(context.Background(), out, "manual")
		if err != nil {
			t.Fatal(err)
		}
		seen[file] = true
	}
	if len(seen) != 5 {
		t.Fatalf("%d distinct backups, want 5", len(seen))
	}
}

func TestBackupDoesNotPruneOtherBackups(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	out := t.TempDir()
	for range 6 {
		if _, err := s.Backup(context.Background(), out, "schema-change"); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 6 {
		t.Fatalf("%d backups kept, want 6: a manual backup must never prune", len(entries))
	}
}

func TestBackupIntoAMissingDirectoryFailsWithoutCreatingIt(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	missing := filepath.Join(t.TempDir(), "nope", "deeper")
	if _, err := s.Backup(context.Background(), missing, "manual"); err == nil {
		t.Fatal("backup into a missing directory succeeded")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the missing directory was created")
	}
}

func TestBackupOfPostgresIsRefused(t *testing.T) {
	s := &Store{dialect: dialect.Postgres}
	if _, err := s.Backup(context.Background(), t.TempDir(), "manual"); !errors.Is(err, ErrBackupUnsupported) {
		t.Fatalf("err = %v, want ErrBackupUnsupported", err)
	}
}

func TestCheckPassesOnAFreshDatabase(t *testing.T) {
	s := newMigrated(t)
	file := s.SQ.File
	s.SQ.Close()
	ro := reopenReadOnly(t, file)
	rep, err := ro.Check(context.Background())
	if err != nil || !rep.OK() || len(rep.Problems) != 0 {
		t.Fatalf("report = %+v %v", rep, err)
	}
}

func TestCheckReportsAMissingColumnWithoutHealingIt(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `ALTER TABLE "apiKeys" DROP COLUMN "accessAllow"`)
	file := s.SQ.File
	s.SQ.Close()
	before, _ := os.ReadFile(file)

	ro := reopenReadOnly(t, file)
	rep, err := ro.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "accessAllow") {
		t.Fatalf("report = %+v", rep)
	}
	ro.SQ.Close()
	after, _ := os.ReadFile(file)
	if string(before) != string(after) {
		t.Fatal("check changed the database file")
	}
	if n := backupNames(t, file); len(n) != 0 {
		t.Fatalf("check made a backup, which means it migrated: %v", n)
	}
}

func TestCheckReportsMissingTableAndIndex(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `DROP INDEX "idx_pc_provider"`)
	s.exec(t, `DROP TABLE "combos"`)
	file := s.SQ.File
	s.SQ.Close()
	ro := reopenReadOnly(t, file)
	rep, err := ro.Check(context.Background())
	if err != nil || rep.OK() {
		t.Fatalf("report = %+v %v", rep, err)
	}
	joined := strings.Join(rep.Problems, "|")
	if !strings.Contains(joined, "combos") || !strings.Contains(joined, "idx_pc_provider") {
		t.Fatalf("problems = %v", rep.Problems)
	}
}

func TestCheckNeverIncludesRowValues(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `INSERT INTO "apiKeys"("id","key","name","machineId","createdAt") VALUES ('k','sk-secretvalue-aaaaaa-bbbbbbbb','n','m','t')`)
	s.exec(t, `DROP TABLE "combos"`)
	file := s.SQ.File
	s.SQ.Close()
	ro := reopenReadOnly(t, file)
	rep, _ := ro.Check(context.Background())
	if strings.Contains(strings.Join(rep.Problems, "")+rep.Integrity, "secretvalue") {
		t.Fatal("a row value leaked into the report")
	}
}

func TestCheckDetectsFileCorruption(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	for i := range 300 {
		s.exec(t, `INSERT INTO "kv"("scope","key","value") VALUES ('scope',?,?)`, strings.Repeat("k", 50)+string(rune('a'+i%26))+strings.Repeat("x", i), `"`+strings.Repeat("v", 200)+`"`)
	}
	file := s.SQ.File
	if err := s.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.SQ.Close()
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := f.Stat()
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = 0xAB
	}
	_, _ = f.WriteAt(garbage, info.Size()/2)
	_ = f.Close()

	ro := reopenReadOnly(t, file)
	rep, err := ro.Check(context.Background())
	if err == nil && rep.OK() {
		t.Fatalf("damaged file reported healthy: %+v", rep)
	}
}

func TestCheckReportsAColumnThatCannotBeAdded(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `ALTER TABLE "providerNodes" DROP COLUMN "data"`)
	file := s.SQ.File
	s.SQ.Close()
	ro := reopenReadOnly(t, file)
	rep, err := ro.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || len(rep.Problems) != 1 || !strings.Contains(rep.Problems[0], "providerNodes") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestCheckReportsAnIndexThatDisagreesWithItsTable(t *testing.T) {
	s := newMigrated(t)
	for _, n := range []string{"alpha", "bravo", "charlie", "delta"} {
		s.exec(t, `INSERT INTO "combos"("id","name","models","createdAt","updatedAt") VALUES (?,?,'[]','t','t')`, n, n)
	}
	if err := s.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	file := s.SQ.File
	s.SQ.Close()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(b), "charlie")
	if i < 0 {
		t.Fatal("test value not found in the file")
	}
	copy(b[i:], "charlix")
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	ro := reopenReadOnly(t, file)
	rep, err := ro.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Integrity == "ok" || rep.OK() {
		t.Fatalf("index/table disagreement not reported: %+v", rep)
	}
}

func TestVacuumShrinksAfterDeletes(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	ctx := context.Background()
	big := strings.Repeat("v", 2000)
	for i := range 400 {
		s.exec(t, `INSERT INTO "kv"("scope","key","value") VALUES ('bulk',?,?)`, strings.Repeat("k", 10)+string(rune('a'+i%26))+strings.Repeat("x", i), `"`+big+`"`)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	full, _ := os.Stat(s.SQ.File)
	s.exec(t, `DELETE FROM "kv" WHERE "scope" = 'bulk'`)
	if err := s.Vacuum(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(s.SQ.File)
	if after.Size() >= full.Size()/2 {
		t.Fatalf("file %d -> %d bytes; vacuum reclaimed nothing", full.Size(), after.Size())
	}
}

func TestCheckpointTruncatesTheWAL(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	ctx := context.Background()
	for i := range 200 {
		s.exec(t, `INSERT INTO "kv"("scope","key","value") VALUES ('scope',?,'1')`, strings.Repeat("k", i+1))
	}
	wal := s.SQ.File + "-wal"
	if info, err := os.Stat(wal); err != nil || info.Size() == 0 {
		t.Skipf("no WAL content to truncate (%v)", err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(wal); err == nil && info.Size() != 0 {
		t.Fatalf("WAL is %d bytes after a TRUNCATE checkpoint", info.Size())
	}
}

func TestOptimizeAndMaintenanceDoNotChangeTheData(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	ctx := context.Background()
	s.exec(t, `INSERT INTO "settings"("id","data") VALUES (1,'{"k":1}')`)
	before := dumpTables(t, s.SQ.File)
	if err := s.Optimize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Analyze(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrementalVacuum(ctx); err != nil {
		t.Fatal(err)
	}
	after := dumpTables(t, s.SQ.File)
	delete(before, "sqlite_stat1")
	delete(after, "sqlite_stat1")
	if diff := diffTables(before, after); len(diff) != 0 {
		t.Fatalf("maintenance changed rows: %v", diff)
	}
}

func TestMaintenanceAfterCloseFailsCleanly(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	s.SQ.Close()
	if err := s.Checkpoint(context.Background()); err == nil {
		t.Fatal("checkpoint on a closed store succeeded")
	}
}

func TestHasConfiguration(t *testing.T) {
	ctx := context.Background()
	fills := map[string]string{
		"connection": `INSERT INTO "providerConnections"("id","provider","authType","data","createdAt","updatedAt") VALUES ('c','p','apikey','{}','t','t')`,
		"api key":    `INSERT INTO "apiKeys"("id","key","createdAt") VALUES ('k','sk-m-aaaaaa-bbbbbbbb','t')`,
		"node":       `INSERT INTO "providerNodes"("id","type","name","data","createdAt","updatedAt") VALUES ('n','t','n','{}','t','t')`,
		"combo":      `INSERT INTO "combos"("id","name","models","createdAt","updatedAt") VALUES ('c','c','[]','t','t')`,
		"kv":         `INSERT INTO "kv"("scope","key","value") VALUES ('scope','k','1')`,
		"setting":    `INSERT INTO "settings"("id","data") VALUES (1,'{"cavemanEnabled":true}')`,
	}
	empty := newMigrated(t)
	if got, err := empty.HasConfiguration(ctx); err != nil || got {
		t.Fatalf("a fresh database: %v %v", got, err)
	}
	empty.exec(t, `INSERT INTO "settings"("id","data") VALUES (1,'{"password":"$2a$10$x"}')`)
	if got, _ := empty.HasConfiguration(ctx); got {
		t.Fatal("a password hash alone counts as configuration")
	}
	for name, fill := range fills {
		s := newMigrated(t)
		s.exec(t, fill)
		if got, err := s.HasConfiguration(ctx); err != nil || !got {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
	corrupt := newMigrated(t)
	corrupt.exec(t, `INSERT INTO "settings"("id","data") VALUES(1,'{broken')`)
	if got, _ := corrupt.HasConfiguration(ctx); !got {
		t.Error("an unreadable settings row must count as content, never as empty")
	}
}

func TestHasConfigurationIgnoresTheCheckRow(t *testing.T) {
	s := newMigrated(t)
	if got, err := s.HasConfiguration(context.Background()); err != nil || got {
		t.Fatalf("the sealing check row counts as configuration: %v %v", got, err)
	}
}

func TestHasConfigurationOfADatabaseWithoutTables(t *testing.T) {
	s := newMigrated(t)
	s.exec(t, `DROP TABLE "combos"`)
	s.exec(t, `DROP TABLE "settings"`)
	if got, err := s.HasConfiguration(context.Background()); err != nil || got {
		t.Fatalf("%v %v", got, err)
	}
}

func TestExportPlainGivesBackTheOriginalValues(t *testing.T) {
	tests.RequireSQLite(t)
	file := makeLegacy(t)
	orig := dumpTables(t, file)
	s := reopen(t, file, tests.DBSecret)
	s.open = testOpener(t)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	exports := filepath.Join(filepath.Dir(file), "exports")
	out, err := s.ExportPlain(context.Background(), exports)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
			t.Fatalf("export mode = %v", fi.Mode().Perm())
		}
		if fi, _ := os.Stat(filepath.Dir(out)); fi.Mode().Perm() != 0o700 {
			t.Fatalf("export dir mode = %v", fi.Mode().Perm())
		}
	}
	got := dumpTables(t, out)
	for _, name := range []string{"apiKeys", "providerConnections", "settings"} {
		if d := diffTables(map[string][]string{name: orig[name]}, map[string][]string{name: got[name]}); len(d) != 0 {
			t.Fatalf("export differs from the original: %v", d)
		}
	}
	ro := tests.OpenSQLiteReadOnly(t, out)
	var n []int64
	if err := ro.DB.Select(&n, `SELECT count(*) FROM "kv" WHERE "scope"=?`, cryptScope); err != nil || n[0] != 0 {
		t.Fatalf("the sealing bookkeeping is still in the export: %v %v", n, err)
	}
}

func TestExportPlainWithTheWrongSecretLeavesNothing(t *testing.T) {
	tests.RequireSQLite(t)
	file := makeLegacy(t)
	s := reopen(t, file, tests.DBSecret)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.SQ.Close()
	wrong := reopen(t, file, tests.OtherDBSecret)
	wrong.open = testOpener(t)
	exports := filepath.Join(filepath.Dir(file), "exports")
	if _, err := wrong.ExportPlain(context.Background(), exports); !errors.Is(err, ErrWrongEncryptionSecret) {
		t.Fatalf("err = %v", err)
	}
	if entries, _ := os.ReadDir(exports); len(entries) != 0 {
		t.Fatalf("a failed export left %d entries", len(entries))
	}
}

func TestExportPlainNeedsAnOpenerAndSQLite(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	if _, err := s.ExportPlain(context.Background(), t.TempDir()); err == nil {
		t.Fatal("export without a way to open the copy succeeded")
	}
	pg := &Store{dialect: dialect.Postgres}
	if _, err := pg.ExportPlain(context.Background(), t.TempDir()); !errors.Is(err, ErrExportUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
