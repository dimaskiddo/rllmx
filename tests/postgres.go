package tests

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	contractsdriver "github.com/goravel/framework/contracts/database/driver"
	"github.com/goravel/postgres"

	"rllmx/app/store/dialect"
)

// PGDSN names the PostgreSQL server the storage suite runs against. It is read
// here and nowhere else, so a test that needs a server asks UsingPostgres, not
// the environment.
const PGDSN = "TEST_PG_DSN"

// UsingPostgres reports the engine the storage suite runs on. One invocation
// runs one engine, so the suite has passed only when both have been run: a test
// green on SQLite and broken here stays invisible until PGDSN is set.
func UsingPostgres() bool { return os.Getenv(PGDSN) != "" }

// Engine is the dialect of the engine the suite runs on, for a test that has no
// connection yet. With a connection, ask Conn.Dialect.
func Engine() dialect.Dialect {
	if UsingPostgres() {
		return dialect.Postgres
	}
	return dialect.SQLite
}

// RequireSQLite skips a test whose subject only exists in a SQLite file: the
// VACUUM INTO copy, a backup, a WAL, a pragma, or a byte-for-byte row dump.
func RequireSQLite(t testing.TB) {
	t.Helper()
	if UsingPostgres() {
		t.Skip("SQLite only: this test reads a file, a pragma or a WAL that PostgreSQL does not have")
	}
}

var pgSeq atomic.Int64

// OpenPostgres opens a connection to the server PGDSN names, in a schema of its
// own, and drops that schema when the test ends. The schema is created here
// because the main store never creates one.
func OpenPostgres(t testing.TB) *Conn {
	t.Helper()
	dsn := os.Getenv(PGDSN)
	if dsn == "" {
		t.Skip(PGDSN + " is not set; the PostgreSQL run needs a server")
	}
	n := pgSeq.Add(1)
	name := fmt.Sprintf("postgres_test_%d", n)
	schema := fmt.Sprintf("rllmx_test_%d", n)
	logger := &Log{}

	// search_path has to be in the DSN: the driver passes a set DSN through
	// unchanged, and the stores' unqualified DDL lands wherever it points.
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	key := "database.connections." + name
	cfg := Config{Values: map[string]any{
		"database.default":             name,
		key + ".driver":                postgres.Name,
		key + ".dsn":                   dsn + sep + "search_path=" + schema,
		key + ".schema":                schema,
		"database.pool.max_open_conns": 4,
		"database.pool.max_idle_conns": 4,
	}}
	driver := postgres.NewPostgres(cfg, logger, nil, name)
	cfg.Values[key+".via"] = func() (contractsdriver.Driver, error) { return driver, nil }
	s, err := assemble(t, cfg, name, driver, logger)
	if err != nil {
		t.Fatalf("tests: open postgres: %v", err)
	}
	s.PGSchema = schema
	// The schema must exist before anything writes into it: the main store
	// never creates one.
	if err := s.DB.Statement(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatalf("tests: create schema: %v", err)
	}
	conn := s.DB
	t.Cleanup(func() {
		// Drop before the pool closes, through the same connection, so the
		// schema goes with the test that made it.
		_ = conn.Statement(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`)
		_ = s.Close()
	})
	return s
}

// OpenAny opens the engine the suite is running on.
func OpenAny(t testing.TB) *Conn {
	t.Helper()
	if UsingPostgres() {
		return OpenPostgres(t)
	}
	return NewSQLite(t)
}
