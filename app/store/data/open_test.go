package data

import (
	"context"
	"path/filepath"
	"rllmx/app/store/dialect"
	"strings"
	"testing"

	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/database/migration"

	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

func TestTheConnectionDependsOnTheEngineAndWhatTheCommandMayWrite(t *testing.T) {
	cases := []struct {
		engine string
		mode   Mode
		want   string
	}{
		{settings.EngineSQLite, ModeWrite, "sqlite"},
		{settings.EngineSQLite, ModeRead, "sqlite_ro"},
		{settings.EngineSQLite, ModeBackup, "sqlite_backup"},
		{settings.EnginePostgres, ModeWrite, "postgres"},
		{settings.EnginePostgres, ModeRead, "postgres"},
		{settings.EnginePostgres, ModeBackup, "postgres"},
	}
	for _, c := range cases {
		if got := Connection(c.engine, c.mode); got != c.want {
			t.Errorf("Connection(%s, %d) = %s, want %s", c.engine, c.mode, got, c.want)
		}
		if _, err := dialect.Of(Connection(c.engine, c.mode)); err != nil {
			t.Errorf("%s is not a connection the repository understands: %v", c.want, err)
		}
	}
}

func TestNewStoreOpensTheDatabaseWithOrWithoutASecret(t *testing.T) {
	sq := tests.NewSQLite(t)
	cfg := &settings.Store{Engine: settings.EngineSQLite, DataDir: filepath.Dir(filepath.Dir(sq.File))}
	deps := Deps{DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Version: "9.9"}

	plain, err := NewStore(deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.SealAPIKey("sk-x"); err != repository.ErrEncryptionSecret {
		t.Fatalf("a store without a secret sealed a key: %v", err)
	}

	raw, _ := hexSecret(t)
	cfg.Secret = raw
	sealed, err := NewStore(deps, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sealed.SealAPIKey("sk-x"); err != nil {
		t.Fatalf("a store with a secret cannot seal: %v", err)
	}
}

func hexSecret(t *testing.T) ([]byte, string) {
	t.Helper()
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(i)
	}
	return b, tests.DBSecret
}

func TestNewStoreMigratesThroughTheFrameworkMigrator(t *testing.T) {
	sq := tests.NewSQLite(t)
	sq.Schema.Register(repository.Migrations(func() contractsschema.Schema { return sq.Schema }, dialect.SQLite))
	raw, _ := hexSecret(t)
	cfg := &settings.Store{Engine: settings.EngineSQLite, DataDir: filepath.Dir(sq.File), Secret: raw}
	s, err := NewStore(Deps{
		DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Version: "t",
		Migrate: func() error { return migration.NewMigrator(nil, sq.Schema, "migrations").Run() },
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if !sq.Schema.HasTable("kv") {
		t.Fatal("the migrator did not create the tables")
	}
}

func TestBackupsGoUnderTheDataDirectory(t *testing.T) {
	sq := tests.NewSQLite(t)
	dir := t.TempDir()
	raw, _ := hexSecret(t)
	cfg := &settings.Store{Engine: settings.EngineSQLite, DataDir: dir, Secret: raw}
	s, err := NewStore(Deps{DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Version: "t", Migrate: func() error {
		return migration.NewMigrator(nil, sq.Schema, "migrations").Run()
	}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	sq.Schema.Register(repository.Migrations(func() contractsschema.Schema { return sq.Schema }, dialect.SQLite))
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	out, err := s.Backup(context.Background(), BackupDir(dir), "manual")
	if err == nil {
		t.Fatalf("the backup directory must exist already: %s", out)
	}
	if want := filepath.Join(dir, "db", "backups"); BackupDir(dir) != want {
		t.Fatalf("BackupDir = %s, want %s", BackupDir(dir), want)
	}
	if !strings.HasSuffix(BackupDir(dir), filepath.Join("db", "backups")) {
		t.Fatal("wrong backup directory")
	}
}

func TestTheMigrationsFollowTheConfiguredEngine(t *testing.T) {
	cfg := tests.Config{Values: map[string]any{"database.default": "postgres"}}
	if d := DialectOf(cfg); d != dialect.Postgres {
		t.Fatalf("dialect %v", d)
	}
	cfg = tests.Config{Values: map[string]any{"database.default": "sqlite"}}
	if d := DialectOf(cfg); d != dialect.SQLite {
		t.Fatalf("dialect %v", d)
	}
	if d := DialectOf(tests.Config{Values: map[string]any{}}); d != dialect.SQLite {
		t.Fatalf("an unset connection is the SQLite default, got %v", d)
	}
}
