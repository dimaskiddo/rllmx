package repository

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/dialect"
	"rllmx/app/sealer"
)

// Options is everything a Store needs. The facades are passed in as their
// interfaces, so a test hands in a real connection over a temp file.
type Options struct {
	// DB is the connection to the configuration database.
	DB contractsdb.DB
	// Schema is the Schema facade bound to the same connection.
	Schema contractsschema.Schema
	Log    log.Log
	// Box seals and opens secrets. It may be nil for a store that only checks,
	// backs up or vacuums; any call that touches a secret then fails with
	// ErrEncryptionSecret.
	Box *sealer.Box
	// Migrate applies the pending framework migrations. Only Migrate needs it.
	Migrate func() error
	// Dialect picks the SQL the store writes.
	Dialect dialect.Dialect
	// PGSchema is the PostgreSQL schema the tables live in; ignored on dialect.SQLite.
	PGSchema string
	// BackupDir receives the automatic backups of a dialect.SQLite database.
	BackupDir string
	// AppVersion is written into backup names and a new database's _meta.
	AppVersion string
	// Open opens another dialect.SQLite file; only ExportPlain needs it.
	Open Opener
}

var (
	// ErrEncryptionSecret means DB_ENCRYPTION_SECRET is not set.
	ErrEncryptionSecret = errors.New("repository: DB_ENCRYPTION_SECRET is required")
	// ErrWrongEncryptionSecret means the secret is not the one the database was
	// sealed with.
	ErrWrongEncryptionSecret = errors.New("repository: DB_ENCRYPTION_SECRET does not match this database")
	// ErrNeedsMigration means the database is missing tables, columns or
	// indexes, or still holds plain secrets. Only the server migrates.
	ErrNeedsMigration = errors.New("repository: the database needs a schema change; start the server once to apply it")
)

// Store is the configuration database.
type Store struct {
	db        contractsdb.DB
	schema    contractsschema.Schema
	log       log.Log
	box       *sealer.Box
	migrate   func() error
	dialect   dialect.Dialect
	pgSchema  string
	backupDir string
	version   string
	lockKey   int64
	open      Opener

	hookMu     sync.RWMutex
	onSettings []func()
}

// New connects to the database and checks that it answers. It writes nothing.
func New(o Options) (*Store, error) {
	if o.DB == nil || o.Schema == nil || o.Log == nil {
		return nil, errors.New("repository: DB, Schema and Log are required")
	}
	if o.AppVersion == "" {
		o.AppVersion = "dev"
	}
	s := &Store{
		db: o.DB, schema: o.Schema, log: o.Log, box: o.Box, migrate: o.Migrate,
		dialect: o.Dialect, pgSchema: o.PGSchema, backupDir: o.BackupDir, version: o.AppVersion,
		lockKey: advisoryKey(o.PGSchema), open: o.Open,
	}
	var one []int64
	if err := o.DB.Select(&one, "SELECT 1"); err != nil {
		return nil, fmt.Errorf("repository: the database does not answer: %w", err)
	}
	return s, nil
}

// dialect.Dialect returns the engine in use.
func (s *Store) Dialect() dialect.Dialect { return s.dialect }

// advisoryKey derives a stable lock id from a schema name, so the main and the
// analytics store never wait on each other.
func advisoryKey(schemaName string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("rllmx:" + schemaName))
	return int64(h.Sum64() >> 1)
}

func (s *Store) on(ctx context.Context) contractsdb.DB { return s.db.WithContext(ctx) }

// Tx runs fn in a transaction. dialect.SQLite takes the write lock at BEGIN (the
// connection is opened with immediate transactions), so a read inside fn
// followed by a write cannot lose to another writer. PostgreSQL serializes
// writers with an advisory lock, the counterpart of dialect.SQLite's single writer.
func (s *Store) Tx(ctx context.Context, fn func(tx contractsdb.Tx) error) error {
	return s.on(ctx).Transaction(func(tx contractsdb.Tx) error {
		if s.dialect == dialect.Postgres {
			if err := tx.Statement(dialect.Rebind(`SELECT pg_advisory_xact_lock(?)`, dialect.Postgres), s.lockKey); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func (s *Store) sel(x contractsdb.Tx, dest any, query string, args ...any) error {
	return x.Select(dest, dialect.Rebind(query, s.dialect), args...)
}

func (s *Store) stmt(x contractsdb.Tx, query string, args ...any) error {
	return x.Statement(dialect.Rebind(query, s.dialect), args...)
}

func (s *Store) count(x contractsdb.Tx, query string, args ...any) (int64, error) {
	var n []int64
	if err := s.sel(x, &n, query, args...); err != nil {
		return 0, err
	}
	if len(n) != 1 {
		return 0, fmt.Errorf("repository: a count query returned %d rows", len(n))
	}
	return n[0], nil
}
