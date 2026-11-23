// Package analytics is the usage store: one metadata-only row per request,
// hourly and daily rollups, and the import of the old usage history. It lives
// in its own database so the compatible main file never grows with it.
package analytics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rllmx/app/store/dialect"
	"runtime"
	"slices"
	"strings"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/platform/file"
	"rllmx/app/store/repository"
)

const (
	// SchemaName is the PostgreSQL schema of the usage store.
	SchemaName = "rllmx_analytics"
	// VacuumPages is how many free pages the daily upkeep returns to the file.
	VacuumPages = 1000

	lifetimeKey   = "lifetimeRequests"
	autoVacuumOn  = 2
	pruneBatch    = 10000
	backupDirName = "analytics-backups"
)

// Options is everything Open needs. The connection is built by the provider
// from config/database.go, after Prepare has made the file; a test hands in an
// already open connection over a temp file.
type Options struct {
	DB      contractsdb.DB
	Schema  contractsschema.Schema
	Log     log.Log
	Dialect dialect.Dialect
	// PGSchema names the PostgreSQL schema; it also separates the store's
	// advisory lock from the main store's.
	PGSchema string
	// File is the SQLite database file, empty on PostgreSQL.
	File string
	// BackupDir receives the copy taken before a schema change.
	BackupDir string
	// Version is written into backup names.
	Version string
	// Open opens another SQLite file, for the legacy import and the drop of the
	// old usage tables.
	Open repository.Opener
}

// Store is the usage database.
type Store struct {
	*repository.Store

	db      contractsdb.DB
	log     log.Log
	file    string
	backups string
	open    repository.Opener
	batch   int
}

// Prepare makes the folder and the file of a SQLite usage database private. It
// runs before the connection is built, because the driver creates the file with
// the process umask when it opens a missing one.
func Prepare(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("analytics: create %s: %w", filepath.Dir(path), err)
	}
	if err := file.TouchPrivate(path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("analytics: restrict %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// schemaName is the PostgreSQL schema the store creates and lives in. It is the
// configured one when a caller sets it, so a test can own a schema of its own;
// the server always passes SchemaName.
func (o Options) schemaName() string {
	if o.PGSchema != "" {
		return o.PGSchema
	}
	return SchemaName
}

// Open connects to the usage store and creates what is missing. It plans the
// additive pass first, so a current database is never written and never backed
// up.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.DB == nil || o.Schema == nil || o.Log == nil {
		return nil, errors.New("analytics: DB, Schema and Log are required")
	}
	base, err := repository.New(repository.Options{
		DB: o.DB, Schema: o.Schema, Log: o.Log, Dialect: o.Dialect,
		PGSchema: o.PGSchema, BackupDir: o.BackupDir, AppVersion: o.Version, Open: o.Open,
	})
	if err != nil {
		return nil, err
	}
	s := &Store{
		Store: base, db: o.DB, log: o.Log, file: o.File, backups: o.BackupDir,
		open: o.Open, batch: pruneBatch,
	}
	if s.backups == "" && o.File != "" {
		s.backups = filepath.Join(filepath.Dir(o.File), backupDirName)
	}
	if o.Dialect == dialect.Postgres {
		if err := o.DB.WithContext(ctx).Statement(`CREATE SCHEMA IF NOT EXISTS "` + o.schemaName() + `"`); err != nil {
			return nil, fmt.Errorf("analytics: create schema: %w", err)
		}
	}
	if err := s.addMissingColumns(ctx); err != nil {
		return nil, err
	}
	if err := s.createSchema(ctx); err != nil {
		return nil, fmt.Errorf("analytics: create schema: %w", err)
	}
	if o.Dialect == dialect.SQLite {
		var mode []int64
		if err := o.DB.WithContext(ctx).Select(&mode, `PRAGMA auto_vacuum`); err == nil && len(mode) == 1 && mode[0] != autoVacuumOn {
			s.log.Warningf("analytics: the usage database was created without incremental auto_vacuum; free pages are returned only by a full vacuum")
		}
	}
	return s, nil
}

// Close checkpoints the SQLite file. The connection pool belongs to the
// framework and lives until the process ends, as the main store's does.
func (s *Store) Close(ctx context.Context) error { return s.Checkpoint(ctx) }

// Path is the SQLite file, empty on PostgreSQL.
func (s *Store) Path() string { return s.file }

func (s *Store) q(query string) string { return dialect.Rebind(query, s.Dialect()) }

var rollupDims = []string{"provider", "model", "connectionId", "apiKeyId", "endpoint"}

func ddl(d dialect.Dialect) []string {
	id, big, real := `"id" INTEGER PRIMARY KEY`, "INTEGER", "REAL"
	if d == dialect.Postgres {
		id, big, real = `"id" BIGSERIAL PRIMARY KEY`, "BIGINT", "DOUBLE PRECISION"
	}
	text := func(n string) string { return `"` + n + `" TEXT NOT NULL DEFAULT ''` }
	num := func(n string) string { return `"` + n + `" ` + big + ` NOT NULL DEFAULT 0` }
	flt := func(n string) string { return `"` + n + `" ` + real + ` NOT NULL DEFAULT 0` }

	rollup := func(name string) string {
		cols := []string{`"bucket" ` + big + ` NOT NULL`}
		for _, c := range rollupDims {
			cols = append(cols, text(c))
		}
		cols = append(cols, num("requests"), num("errors"), num("promptTokens"), num("completionTokens"),
			num("cachedTokens"), num("cacheWriteTokens"), num("reasoningTokens"), flt("cost"),
			`PRIMARY KEY ("bucket","provider","model","connectionId","apiKeyId","endpoint")`)
		return `CREATE TABLE IF NOT EXISTS "` + name + `" (` + strings.Join(cols, ", ") + `)`
	}
	requests := strings.Join([]string{
		id, `"ts" ` + big + ` NOT NULL`, text("requestId"), text("apiKeyId"), text("apiKeyName"), text("connectionId"),
		text("provider"), text("model"), text("endpoint"), text("sourceFormat"),
		`"status" ` + big + ` NOT NULL DEFAULT 0`, text("errorClass"),
		num("stream"), num("cached"), num("estimated"), num("promptTokens"), num("completionTokens"), num("cachedTokens"),
		num("cacheWriteTokens"), num("reasoningTokens"), flt("cost"), num("latencyMs"), num("ttfbMs"),
		text("comboName"), num("attempt"), text("savers"), text("skills"), `"importId" TEXT UNIQUE`,
	}, ", ")
	return []string{
		`CREATE TABLE IF NOT EXISTS "requests" (` + requests + `)`,
		`CREATE INDEX IF NOT EXISTS "idx_req_ts" ON "requests" ("ts")`,
		`CREATE INDEX IF NOT EXISTS "idx_req_key_ts" ON "requests" ("apiKeyId","ts")`,
		`CREATE INDEX IF NOT EXISTS "idx_req_conn_ts" ON "requests" ("connectionId","ts")`,
		`CREATE INDEX IF NOT EXISTS "idx_req_model_ts" ON "requests" ("provider","model","ts")`,
		rollup("rollupHourly"),
		rollup("rollupDaily"),
		`CREATE TABLE IF NOT EXISTS "meta" ("key" TEXT PRIMARY KEY, "value" ` + big + ` NOT NULL)`,
	}
}

// addedColumns are columns an older database lacks; they get ALTER TABLE, a new
// database gets them from the DDL.
var addedColumns = []struct{ table, column string }{
	{"requests", "estimated"},
	{"requests", "savers"},
	{"requests", "skills"},
}

// addMissingColumns plans first: when nothing is missing it writes nothing. A
// SQLite file is backed up before the first change, and a failed backup aborts.
func (s *Store) addMissingColumns(ctx context.Context) error {
	var missing []struct{ table, column string }
	for _, c := range addedColumns {
		exists, hasCol, err := s.columnState(ctx, c.table, c.column)
		if err != nil {
			return fmt.Errorf("analytics: inspect %s: %w", c.table, err)
		}
		if exists && !hasCol {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if s.Dialect() == dialect.SQLite {
		if err := os.MkdirAll(s.backups, 0o700); err != nil {
			return fmt.Errorf("analytics: backup before the schema change failed, nothing was changed: %w", err)
		}
		if _, err := s.Backup(ctx, s.backups, "schema-change"); err != nil {
			return fmt.Errorf("analytics: backup before the schema change failed, nothing was changed: %w", err)
		}
	}
	return s.Tx(ctx, func(tx contractsdb.Tx) error {
		for _, c := range missing {
			stmt := `ALTER TABLE "` + c.table + `" ADD COLUMN "` + c.column + `" ` + s.bigType() + ` NOT NULL DEFAULT 0`
			if err := tx.Statement(stmt); err != nil {
				return fmt.Errorf("analytics: add column %s.%s: %w", c.table, c.column, err)
			}
		}
		return nil
	})
}

func (s *Store) bigType() string {
	if s.Dialect() == dialect.Postgres {
		return "BIGINT"
	}
	return "INTEGER"
}

// columnState reports whether the table exists and holds the column. The
// column list comes from the repository, which owns the dialect switch, so
// there is one query and not two. An error is an error, never "missing".
func (s *Store) columnState(ctx context.Context, table, column string) (exists, hasCol bool, err error) {
	names, err := s.ColumnNames(ctx, table)
	if err != nil {
		return false, false, err
	}
	return len(names) > 0, slices.Contains(names, column), nil
}

func (s *Store) createSchema(ctx context.Context) error {
	return s.Tx(ctx, func(tx contractsdb.Tx) error {
		for _, stmt := range ddl(s.Dialect()) {
			if err := tx.Statement(stmt); err != nil {
				return err
			}
		}
		return nil
	})
}
