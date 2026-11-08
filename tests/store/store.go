// Package store gives the tests of other packages a repository.Store over a real
// temp database, built the way the data provider builds one. Repository's own
// tests cannot import it (it imports repository), so they keep a small helper of
// their own.
package store

import (
	"context"
	"path/filepath"
	"testing"

	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/database/migration"

	"rllmx/app/repository"
	"rllmx/app/sealer"
	"rllmx/tests"
)

// Store is a repository store with the connection under it.
type Store struct {
	*repository.Store
	SQ *tests.Conn
}

func boxFor(t testing.TB, secret string) *sealer.Box {
	if secret == "" {
		return nil
	}
	return tests.Box(t, secret)
}

// Over builds a store on a connection the test opened. The migrations are
// registered on that connection's Schema, as WithMigrations does.
func Over(t testing.TB, sq *tests.Conn, secret string) *Store {
	t.Helper()
	d := tests.Engine()
	sq.Schema.Register(repository.Migrations(func() contractsschema.Schema { return sq.Schema }, d))
	s, err := repository.New(repository.Options{
		DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Box: boxFor(t, secret), Dialect: d,
		PGSchema: sq.PGSchema,
		Migrate:  func() error { return migration.NewMigrator(nil, sq.Schema, "migrations").Run() },
		// A SQLite store backs up before a change; PostgreSQL is told to use
		// pg_dump instead, so it has no folder to name.
		BackupDir:  sqliteBackupDir(sq),
		AppVersion: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Store{Store: s, SQ: sq}
}

func sqliteBackupDir(sq *tests.Conn) string {
	if sq.File == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(sq.File), "backups")
}

// New is an empty database with no tables, sealed under tests.DBSecret.
func New(t testing.TB) *Store {
	t.Helper()
	return Over(t, tests.OpenAny(t), tests.DBSecret)
}

// Migrated is New after Migrate.
func Migrated(t testing.TB) *Store {
	t.Helper()
	s := New(t)
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("store: migrate: %v", err)
	}
	return s
}

// Opener opens a SQLite file the way app/providers does in production: every
// call gets its own connection name and its pool closes through Conn.Close.
func Opener(t testing.TB) repository.Opener {
	t.Helper()
	return func(path, query string) (*repository.Conn, error) {
		sq := tests.OpenSQLiteWithQuery(t, path, query)
		return &repository.Conn{DB: sq.DB, Schema: sq.Schema, Close: sq.Close}, nil
	}
}
