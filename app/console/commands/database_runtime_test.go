package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"

	"rllmx/app/store/data"
	"rllmx/tests"
	"rllmx/tests/store"
)

func facadesFor(t *testing.T, file string, over map[string]any) func() Facades {
	t.Helper()
	sq := tests.OpenSQLite(t, file)
	m := map[string]any{
		"database.default":                     "sqlite",
		"database.connections.sqlite.database": file,
		"app.data_dir":                         filepath.Dir(filepath.Dir(file)),
		"database.analytics.retention_days":    90,
	}
	for k, v := range over {
		m[k] = v
	}
	return func() Facades {
		return Facades{Config: tests.Config{Values: m}, Log: sq.Log, Connect: func(string) (contractsdb.DB, contractsschema.Schema) { return sq.DB, sq.Schema }}
	}
}

func TestACommandOpensAnExistingDatabaseWithoutASecret(t *testing.T) {
	st := store.Migrated(t)
	st.SQ.Close()
	rt := NewDatabaseRuntime(facadesFor(t, st.SQ.File, nil), "t")
	h, err := rt.Open(context.Background(), data.ModeRead)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := h.Store.Check(context.Background())
	if err != nil || !rep.OK() {
		t.Fatalf("check = %+v %v", rep, err)
	}
}

func TestACommandNeverCreatesTheDatabase(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "db", "data.sqlite")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	rt := NewDatabaseRuntime(func() Facades {
		return Facades{Config: tests.Config{Values: map[string]any{
			"database.default": "sqlite", "database.connections.sqlite.database": file, "app.data_dir": dir,
			"database.analytics.retention_days": 90,
		}}}
	}, "t")
	if _, err := rt.Open(context.Background(), data.ModeWrite); err == nil || !strings.Contains(err.Error(), "no database") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a command created the database file")
	}
}

func TestACommandRefusesAnEmptyDatabaseFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "data.sqlite")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	rt := NewDatabaseRuntime(func() Facades {
		return Facades{Config: tests.Config{Values: map[string]any{
			"database.default": "sqlite", "database.connections.sqlite.database": file, "app.data_dir": dir,
			"database.analytics.retention_days": 90,
		}}}
	}, "t")
	if _, err := rt.Open(context.Background(), data.ModeRead); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestACommandRejectsABadConfigurationWithoutQuotingIt(t *testing.T) {
	st := store.Migrated(t)
	st.SQ.Close()
	const marker = "zz-bad-secret-marker"
	rt := NewDatabaseRuntime(facadesFor(t, st.SQ.File, map[string]any{"database.encryption_secret": marker}), "t")
	_, err := rt.Open(context.Background(), data.ModeRead)
	if err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") || strings.Contains(err.Error(), marker) {
		t.Fatalf("err = %v", err)
	}
	rt = NewDatabaseRuntime(facadesFor(t, st.SQ.File, map[string]any{"database.default": "mysql"}), "t")
	if _, err := rt.Open(context.Background(), data.ModeRead); err == nil || !strings.Contains(err.Error(), "DB_CONNECTION") {
		t.Fatalf("err = %v", err)
	}
}

func TestTheTerminalCheckIsAFunction(t *testing.T) {
	rt := NewDatabaseRuntime(func() Facades { return Facades{} }, "t")
	if rt.Terminal == nil {
		t.Fatal("no terminal check")
	}
	_ = rt.Terminal()
}

// The schema command fills the empty file the server's start-up leaves, and
// nothing else: a path nobody prepared is a typo, and opening it would create a
// database with the process's umask beside a deployment that has its own.
func TestTheSchemaCommandOpensAnEmptyFileAndRefusesAMissingOne(t *testing.T) {
	st := store.Migrated(t)
	st.SQ.Close()
	dir := t.TempDir()
	missing := filepath.Join(dir, "typo.sqlite")
	empty := filepath.Join(dir, "empty.sqlite")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	rt := NewDatabaseRuntime(facadesFor(t, st.SQ.File, map[string]any{"database.connections.sqlite.database": empty}), "t")
	if _, err := rt.Open(context.Background(), data.ModeInit); err != nil {
		t.Fatalf("an empty file was refused: %v", err)
	}
	if _, err := rt.Open(context.Background(), data.ModeWrite); err == nil {
		t.Fatal("a data command opened an empty database")
	}

	rt = NewDatabaseRuntime(facadesFor(t, st.SQ.File, map[string]any{"database.connections.sqlite.database": missing}), "t")
	if _, err := rt.Open(context.Background(), data.ModeInit); err == nil {
		t.Fatal("the schema command opened a path with no file")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("the schema command created a database file")
	}
}

func TestTheSchemaCommandMigratesAFreshFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "db", "data.sqlite")
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	sq := tests.OpenSQLite(t, file)
	_ = store.Over(t, sq, tests.DBSecret)
	rt := NewDatabaseRuntime(func() Facades {
		return Facades{
			Config: tests.Config{Values: map[string]any{
				"database.default":                     "sqlite",
				"database.connections.sqlite.database": file,
				"app.data_dir":                         dir,
				"database.analytics.retention_days":    90,
				"database.encryption_secret":           tests.DBSecret,
				"database.migrations.table":            "migrations",
			}},
			Log:     sq.Log,
			Connect: func(string) (contractsdb.DB, contractsschema.Schema) { return sq.DB, sq.Schema },
			Schema:  func() contractsschema.Schema { return sq.Schema },
		}
	}, "t")
	h, err := rt.Open(context.Background(), data.ModeInit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Store.Migrate(context.Background()); err != nil {
		t.Fatalf("mode init: %v", err)
	}
	missing, err := h.Store.NeedsMigration(context.Background())
	if err != nil || missing {
		t.Fatalf("needs migration = %v, %v", missing, err)
	}
}

func TestTheModeDecidesWhichConnectionTheCommandOpens(t *testing.T) {
	st := store.Migrated(t)
	st.SQ.Close()
	for mode, want := range map[data.Mode]string{
		data.ModeWrite: "sqlite", data.ModeRead: "sqlite_ro", data.ModeBackup: "sqlite_backup", data.ModeInit: "sqlite",
	} {
		var asked string
		sq := tests.OpenSQLite(t, st.SQ.File)
		rt := NewDatabaseRuntime(func() Facades {
			return Facades{
				Config: tests.Config{Values: map[string]any{
					"database.default": "sqlite", "database.connections.sqlite.database": st.SQ.File,
					"app.data_dir": filepath.Dir(filepath.Dir(st.SQ.File)), "database.analytics.retention_days": 90,
				}},
				Log: sq.Log,
				Connect: func(name string) (contractsdb.DB, contractsschema.Schema) {
					asked = name
					return sq.DB, sq.Schema
				},
			}
		}, "t")
		if _, err := rt.Open(context.Background(), mode); err != nil {
			t.Fatal(err)
		}
		if asked != want {
			t.Errorf("mode %d opened %q, want %q", mode, asked, want)
		}
	}
}
