package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/gateway/skill"
	"rllmx/app/store/lock"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// legacySource writes a small legacy database: the configuration tables the
// import reads plus usage, a request-details table and the fork's fitness table
// the drop must leave alone.
func legacySource(t *testing.T) string {
	t.Helper()
	sq := tests.NewSQLite(t)
	defer sq.Close()
	for _, stmt := range []string{
		`CREATE TABLE "_meta"("key" TEXT PRIMARY KEY, "value" TEXT NOT NULL)`,
		`CREATE TABLE "settings"("id" INTEGER PRIMARY KEY CHECK (id = 1), "data" TEXT NOT NULL)`,
		`CREATE TABLE "providerConnections"("id" TEXT PRIMARY KEY, "provider" TEXT NOT NULL, "authType" TEXT NOT NULL, "name" TEXT, "email" TEXT, "priority" INTEGER, "isActive" INTEGER DEFAULT 1, "data" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL)`,
		`CREATE TABLE "providerNodes"("id" TEXT PRIMARY KEY, "type" TEXT, "name" TEXT, "data" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL)`,
		`CREATE TABLE "apiKeys"("id" TEXT PRIMARY KEY, "key" TEXT NOT NULL UNIQUE, "name" TEXT, "machineId" TEXT, "isActive" INTEGER DEFAULT 1, "createdAt" TEXT NOT NULL, "accessRestricted" INTEGER DEFAULT 0, "accessAllow" TEXT)`,
		`CREATE TABLE "combos"("id" TEXT PRIMARY KEY, "name" TEXT NOT NULL UNIQUE, "kind" TEXT, "models" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL)`,
		`CREATE TABLE "kv"("scope" TEXT NOT NULL, "key" TEXT NOT NULL, "value" TEXT NOT NULL, PRIMARY KEY ("scope","key"))`,
		`CREATE TABLE "usageHistory"("id" INTEGER PRIMARY KEY AUTOINCREMENT, "timestamp" TEXT NOT NULL, "provider" TEXT, "model" TEXT, "connectionId" TEXT, "apiKey" TEXT, "endpoint" TEXT, "promptTokens" INTEGER DEFAULT 0, "completionTokens" INTEGER DEFAULT 0, "cost" REAL DEFAULT 0, "status" TEXT, "tokens" TEXT, "meta" TEXT)`,
		`CREATE TABLE "usageDaily"("dateKey" TEXT PRIMARY KEY, "data" TEXT NOT NULL)`,
		`CREATE TABLE "requestDetails"("id" INTEGER PRIMARY KEY AUTOINCREMENT, "timestamp" TEXT, "provider" TEXT)`,
		`CREATE TABLE "proxyPoolFitness"("poolId" TEXT NOT NULL, "scope" TEXT NOT NULL, "until" TEXT, PRIMARY KEY ("poolId","scope"))`,
		`INSERT INTO "_meta"("key","value") VALUES('totalRequestsLifetime','42')`,
		`INSERT INTO "settings"("id","data") VALUES(1,'{"stickyRoundRobinLimit":4,"authMode":"password","password":"$2a$10$x"}')`,
		`INSERT INTO "providerConnections"("id","provider","authType","name","email","priority","isActive","data","createdAt","updatedAt") VALUES('c1','openai','apikey','work','w@example.test',1,1,'{"apiKey":"sk-up","testStatus":"active","modelLock_gpt-5":"2026-01-01T00:00:00.000Z","stickyCount":1}','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`,
		`INSERT INTO "apiKeys"("id","key","name","machineId","isActive","createdAt","accessRestricted","accessAllow") VALUES('k1','sk-alpha-111111-aaaa','ci','m1',1,'2026-01-01T00:00:00.000Z',1,'["openai/*"]')`,
		`INSERT INTO "providerNodes"("id","type","name","data","createdAt","updatedAt") VALUES('n1','openai-compatible','local','{"prefix":"dke"}','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`,
		`INSERT INTO "combos"("id","name","kind","models","createdAt","updatedAt") VALUES('b1','fast',NULL,'["openai/gpt-5"]','2026-01-01T00:00:00.000Z','2026-01-01T00:00:00.000Z')`,
		`INSERT INTO "kv"("scope","key","value") VALUES('modelAliases','opus','"openai/gpt-5"')`,
		`INSERT INTO "usageHistory"("timestamp","provider","model","apiKey","promptTokens","completionTokens","cost","status","tokens") VALUES('2026-01-01T00:00:00.000Z','openai','gpt-5','sk-alpha-111111-aaaa',10,20,0.5,'ok','{"cached_tokens":3}')`,
		`INSERT INTO "usageHistory"("timestamp","provider","model","apiKey","promptTokens","completionTokens","cost","status","tokens") VALUES('2026-01-02T00:00:00.000Z','openai','gpt-5',NULL,1,2,0.25,'429','{}')`,
	} {
		if err := sq.DB.Statement(stmt); err != nil {
			t.Fatalf("source: %s: %v", stmt, err)
		}
	}
	return sq.File
}

func TestImportConfigDryRunWritesNothing(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"dry-run": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.output(), "dry run: nothing was written") {
		t.Fatalf("output %q", c.output())
	}
	if !strings.Contains(c.output(), "1 provider connections") {
		t.Fatalf("no plan printed: %q", c.output())
	}
	var n []int64
	if err := r.dest.SQ.DB.Select(&n, `SELECT count(*) FROM "providerConnections"`); err != nil {
		t.Fatal(err)
	}
	if n[0] != 0 {
		t.Fatal("the dry run wrote a connection")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "db", "import-tmp", "leftover")); err == nil {
		t.Fatal("the dry run left a temporary directory")
	}
}

func TestImportConfigWithYesAppliesAndVerifies(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"yes": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.output(), "verified: wrote 1 connections, 1 API keys, 1 nodes, 1 combos, 1 kv rows, 1 settings") {
		t.Fatalf("output %q", c.output())
	}
	var keys []string
	if err := r.dest.SQ.DB.Select(&keys, `SELECT "key" FROM "apiKeys"`); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "enc:v1:") {
		t.Fatalf("keys = %v", keys)
	}
}

func TestImportConfigAsksAndAcceptsOnlyYes(t *testing.T) {
	for answer, want := range map[string]bool{
		"y": true, "yes": true, "Y": true, "YES": true,
		"n": false, "": false, "sure": false, " y": true,
	} {
		t.Run(answer, func(t *testing.T) {
			r := newImportRig(t)
			src := legacySource(t)
			c := &importConsole{dbConsole{opts: map[string]string{"from": src}, answers: []string{answer}}}
			err := (&DBImportConfig{R: r.runtime()}).Handle(c)
			var n []int64
			if err := r.dest.SQ.DB.Select(&n, `SELECT count(*) FROM "apiKeys"`); err != nil {
				t.Fatal(err)
			}
			if want && (err != nil || n[0] != 1) {
				t.Fatalf("answer %q: err %v, %d rows", answer, err, n[0])
			}
			if !want && (err == nil || n[0] != 0) {
				t.Fatalf("answer %q: err %v, %d rows", answer, err, n[0])
			}
		})
	}
}

func TestImportConfigWithoutATerminalNeedsYes(t *testing.T) {
	r := newImportRig(t)
	r.tty = false
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}}}
	err := (&DBImportConfig{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportConfigRefusesANonEmptyDestination(t *testing.T) {
	r := newImportRig(t)
	if err := r.dest.SQ.DB.Statement(`INSERT INTO "kv"("scope","key","value") VALUES('modelAliases','x','"y"')`); err != nil {
		t.Fatal(err)
	}
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"yes": true}}}
	err := (&DBImportConfig{R: r.runtime()}).Handle(c)
	if err == nil || !strings.Contains(err.Error(), "only seeds an empty one") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportConfigNeedsTheDestinationSchema(t *testing.T) {
	for name, flags := range map[string]map[string]bool{
		"apply":   {"yes": true},
		"dry run": {"dry-run": true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newImportRigWithoutSchema(t)
			c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: flags}}
			err := (&DBImportConfig{R: r.runtime()}).Handle(c)
			if !errors.Is(err, repository.ErrNeedsMigration) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

// The skills copy is not part of the database transaction, so nothing may be
// copied until the destination is known to accept the rest.
func TestImportConfigCopiesNoSkillsIntoADestinationWithoutTheSchema(t *testing.T) {
	r := newImportRigWithoutSchema(t)
	c := &importConsole{dbConsole{
		opts:  map[string]string{"from": legacySource(t), "skills-from": skillsSource(t)},
		flags: map[string]bool{"yes": true},
	}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); !errors.Is(err, repository.ErrNeedsMigration) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "skills")); err == nil {
		t.Fatal("skills were copied although the import could not run")
	}
}

// The server's start-up leaves a 0-byte database file behind; it is not a
// destination, and a dry run must not call it one.
func TestImportConfigRefusesAnEmptyDatabaseFile(t *testing.T) {
	for name, flags := range map[string]map[string]bool{
		"apply":   {"yes": true},
		"dry run": {"dry-run": true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newImportRig(t)
			empty := filepath.Join(r.dir, "empty.sqlite")
			if err := os.WriteFile(empty, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			rt := r.runtime()
			inner := rt.Facades
			rt.Facades = func() Facades {
				fc := inner()
				fc.Config = tests.Config{Values: map[string]any{
					"database.default":                     "sqlite",
					"database.connections.sqlite.database": empty,
					"app.data_dir":                         r.dir,
					"database.encryption_secret":           tests.DBSecret,
				}}
				return fc
			}
			c := &importConsole{dbConsole{
				opts:  map[string]string{"from": legacySource(t), "skills-from": skillsSource(t)},
				flags: flags,
			}}
			err := (&DBImportConfig{R: rt}).Handle(c)
			if err == nil || !strings.Contains(err.Error(), "db:migrate") {
				t.Fatalf("err = %v", err)
			}
			if _, err := os.Stat(filepath.Join(r.dir, "skills")); err == nil {
				t.Fatal("skills were copied into an unusable destination")
			}
		})
	}
}

func TestImportConfigAfterMigrate(t *testing.T) {
	r := newImportRigWithoutSchema(t)
	if err := (&DBMigrate{R: r.runtime()}).Handle(&dbConsole{}); err != nil {
		t.Fatal(err)
	}
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"yes": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	var n []int64
	if err := r.dest.SQ.DB.Select(&n, `SELECT count(*) FROM "providerConnections"`); err != nil || n[0] != 1 {
		t.Fatalf("connections = %v, %v", n, err)
	}
}

func TestImportConfigDryRunOnANonEmptyDestinationSaysSo(t *testing.T) {
	r := newImportRig(t)
	if err := r.dest.SQ.DB.Statement(`INSERT INTO "kv"("scope","key","value") VALUES('modelAliases','x','"y"')`); err != nil {
		t.Fatal(err)
	}
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"dry-run": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err == nil || !strings.Contains(err.Error(), "only seeds an empty one") {
		t.Fatalf("err = %v", err)
	}
}

func TestImportConfigWithSSOCopiesThePolicy(t *testing.T) {
	r := newImportRig(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"yes": true, "with-sso": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	d, err := r.dest.Settings().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetString("authMode"); v != "password" {
		t.Fatalf("authMode = %q", v)
	}
	if v, _ := d.GetString("password"); v != "" {
		t.Fatalf("the old password travelled: %q", v)
	}
}

func TestImportConfigAcceptsAFileOrADataDirectory(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	dir := filepath.Dir(src)
	for _, from := range []string{src, dir} {
		c := &importConsole{dbConsole{opts: map[string]string{"from": from}, flags: map[string]bool{"dry-run": true}}}
		if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
			t.Fatalf("%s: %v", from, err)
		}
	}
}

func TestImportConfigNeedsASourceThatExists(t *testing.T) {
	r := newImportRig(t)
	for _, from := range []string{"", filepath.Join(t.TempDir(), "nope.sqlite")} {
		c := &importConsole{dbConsole{opts: map[string]string{"from": from}, flags: map[string]bool{"yes": true}}}
		if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err == nil {
			t.Fatalf("%q was accepted", from)
		}
	}
}

func TestImportConfigNeverModifiesTheSource(t *testing.T) {
	r := newImportRig(t)
	src := legacySource(t)
	before, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	c := &importConsole{dbConsole{opts: map[string]string{"from": src}, flags: map[string]bool{"yes": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(src)
	if err != nil {
		t.Fatal(err)
	}
	now, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || string(data) != string(now) {
		t.Fatal("the source changed")
	}
}

func TestImportConfigLeavesNoTemporaryDirectoryAfterAnyOutcome(t *testing.T) {
	for name, flags := range map[string]map[string]bool{
		"applied":  {"yes": true},
		"declined": {},
	} {
		t.Run(name, func(t *testing.T) {
			r := newImportRig(t)
			c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: flags, answers: []string{"n"}}}
			_ = (&DBImportConfig{R: r.runtime()}).Handle(c)
			if _, err := os.Stat(filepath.Join(r.dir, "db", "import-tmp")); !os.IsNotExist(err) {
				t.Fatalf("the copy folder the command created is still there: %v", err)
			}
		})
	}
}

// A folder that was already there belongs to the operator and stays, even after
// a successful import.
func TestImportConfigKeepsAFolderThatAlreadyExisted(t *testing.T) {
	r := newImportRig(t)
	dir := filepath.Join(r.dir, "db", "import-tmp")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"yes": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("the existing folder was removed: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Fatalf("a copy was left behind: %s", e.Name())
	}
}

func TestImportConfigRefusesWhileTheLockIsHeld(t *testing.T) {
	r := newImportRig(t)
	held, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"yes": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v", err)
	}
}

func TestImportConfigReleasesTheLockAfterwards(t *testing.T) {
	r := newImportRig(t)
	c := &importConsole{dbConsole{opts: map[string]string{"from": legacySource(t)}, flags: map[string]bool{"yes": true}}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	l, err := lock.Acquire(r.dir)
	if err != nil {
		t.Fatalf("the lock stayed held: %v", err)
	}
	_ = l.Release()
}

func TestImportConfigDeclaresItsFlags(t *testing.T) {
	got := map[string]bool{}
	for _, f := range (&DBImportConfig{}).Extend().Flags {
		got[flagName(f)] = true
	}
	for _, want := range []string{"from", "dry-run", "yes", "with-sso"} {
		if !got[want] {
			t.Errorf("%s is not declared: %v", want, got)
		}
	}
	if (&DBImportConfig{}).Signature() != "db:import-config" {
		t.Fatalf("signature %q", (&DBImportConfig{}).Signature())
	}
}

// skillsSource is a folder of skills holding one that acts only on a rule.
func skillsSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	for name, body := range map[string]string{
		"terse/manifest.json":  skillManifest,
		"terse/prompt.txt":     "Answer briefly.",
		"router/manifest.json": `{"id":"router","name":"Router","description":"reroutes","hook":"pre-route"}`,
		"router/rules.json":    `{"rules":[{"stage":"pre-route","when":{"modelRegex":"^m$"},"then":{"rerouteModel":"wire/short"}}]}`,
	} {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestImportConfigWritesNoSkillsWhenTheOperatorDeclines(t *testing.T) {
	r := newImportRig(t)
	c := &importConsole{dbConsole{
		opts:    map[string]string{"from": legacySource(t), "skills-from": skillsSource(t)},
		answers: []string{"n"},
	}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err == nil {
		t.Fatal("a declined import did not stop")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "skills")); err == nil {
		t.Fatal("skills were copied although the operator declined")
	}
}

func TestImportConfigCopiesSkillsAfterTheConfirmationWithTheirRules(t *testing.T) {
	r := newImportRig(t)
	c := &importConsole{dbConsole{
		opts:  map[string]string{"from": legacySource(t), "skills-from": skillsSource(t)},
		flags: map[string]bool{"yes": true},
	}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	reg := skill.NewRegistry(filepath.Join(r.dir, "skills"), nil)
	if _, ok := reg.Find("terse"); !ok {
		t.Fatalf("terse missing: %v", reg.Problems())
	}
	if _, ok := reg.Find("router"); !ok {
		t.Fatalf("a rule-only skill did not load after the copy: %v", reg.Problems())
	}
}

func TestImportConfigDryRunListsSkillsAndWritesNone(t *testing.T) {
	r := newImportRig(t)
	c := &importConsole{dbConsole{
		opts:  map[string]string{"from": legacySource(t), "skills-from": skillsSource(t)},
		flags: map[string]bool{"dry-run": true},
	}}
	if err := (&DBImportConfig{R: r.runtime()}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.output(), "would copy terse") {
		t.Fatalf("the dry run did not list the skills: %q", c.output())
	}
	if _, err := os.Stat(filepath.Join(r.dir, "skills")); err == nil {
		t.Fatal("a dry run wrote skills")
	}
}
