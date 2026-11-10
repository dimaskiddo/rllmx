package tests

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsdriver "github.com/goravel/framework/contracts/database/driver"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/sqlite"

	"rllmx/app/platform/file"
)

var connectionSeq atomic.Int64

// Conn is a real Goravel connection a test opened, on either engine. DB and
// Schema are built the way the framework builds them, over the same pooled
// connection.
type Conn struct {
	DB     contractsdb.DB
	Schema contractsschema.Schema
	Log    *Log
	// File is the SQLite file, empty on PostgreSQL.
	File string
	// PGSchema is the PostgreSQL schema the test owns, empty on SQLite.
	PGSchema string
	Name     string

	closeDB func() error
}

// Close releases the connection pool, so the file can be removed on every
// platform. It is safe to call twice.
func (s *Conn) Close() error {
	if s.closeDB == nil {
		return nil
	}
	err := s.closeDB()
	s.closeDB = nil
	return err
}

// NewSQLite builds a connection the way the framework does for production:
// BuildDB over the official driver. Every call gets its own connection name,
// because the framework caches built connections by name for the whole process.
// The file lives in t.TempDir(); an in-memory database is not used, since every
// pooled connection would see its own empty one.
func NewSQLite(t testing.TB) *Conn {
	t.Helper()
	return OpenSQLite(t, filepath.Join(t.TempDir(), "data.sqlite"))
}

// OpenSQLite is NewSQLite over a file the test chose, for example a copy of a
// fixture database.
func OpenSQLite(t testing.TB, path string) *Conn {
	t.Helper()
	return open(t, path, writableDSN(path))
}

// OpenSQLiteReadOnly opens an existing file for reading only, the way the
// sqlite_ro connection does: a write fails in SQLite itself.
func OpenSQLiteReadOnly(t testing.TB, path string) *Conn {
	t.Helper()
	return OpenSQLiteWithQuery(t, path, "mode=ro&_pragma=busy_timeout(5000)&_pragma=query_only(1)")
}

// OpenSQLiteWithQuery opens path with the given DSN query string, for a test
// that needs a setting the other openers do not give, such as a short busy wait.
func OpenSQLiteWithQuery(t testing.TB, path, query string) *Conn {
	t.Helper()
	return open(t, path, file.URI(path)+"?"+query)
}

func open(t testing.TB, path, dsn string) *Conn {
	t.Helper()
	name := fmt.Sprintf("sqlite_test_%d", connectionSeq.Add(1))
	logger := &Log{}
	key := "database.connections." + name
	cfg := Config{Values: map[string]any{
		"database.default":             name,
		key + ".driver":                sqlite.Name,
		key + ".database":              path,
		key + ".dsn":                   dsn,
		"database.pool.max_open_conns": 4,
		"database.pool.max_idle_conns": 4,
	}}
	driver := sqlite.NewSqlite(cfg, logger, name)
	cfg.Values[key+".via"] = func() (contractsdriver.Driver, error) { return driver, nil }
	s, err := assemble(t, cfg, name, driver, logger)
	if err != nil {
		t.Fatalf("tests: open sqlite: %v", err)
	}
	s.File = path
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func writableDSN(path string) string {
	return file.URI(path) + "?_txlock=immediate&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=secure_delete(ON)"
}
