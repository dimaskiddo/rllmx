package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

var (
	// ErrBackupUnsupported is returned for PostgreSQL, which has no file to copy.
	ErrBackupUnsupported = errors.New("repository: PostgreSQL has no file backup; use pg_dump")
	// ErrExportUnsupported is returned for PostgreSQL: the programs this one
	// replaces read only dialect.SQLite files.
	ErrExportUnsupported = errors.New("repository: export-plain needs dialect.SQLite; the previous program reads only dialect.SQLite files")
)

// Conn is an extra connection to a dialect.SQLite file, opened through the framework.
// Close releases its pool.
type Conn struct {
	DB     contractsdb.DB
	Schema contractsschema.Schema
	Close  func() error
}

// Opener opens a dialect.SQLite file the Store does not own, with a DSN query string.
// The provider builds one over the framework's connection factory.
type Opener func(path, query string) (*Conn, error)

// Backup writes a full copy of the dialect.SQLite database into a new directory under
// dir, which must already exist, and returns the file path. Nothing is pruned.
func (s *Store) Backup(ctx context.Context, dir, label string) (string, error) {
	if s.dialect != dialect.SQLite {
		return "", ErrBackupUnsupported
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", fmt.Errorf("repository: backup directory %s does not exist", dir)
	}
	return s.backup(ctx, dir, label)
}

// CheckReport is the outcome of Check. It names tables, columns and indexes
// but never a row value.
type CheckReport struct {
	Integrity string
	Problems  []string
}

// OK reports whether the database is healthy and matches the expected schema.
func (r CheckReport) OK() bool {
	return r.Integrity == "ok" && len(r.Problems) == 0
}

// Check runs the full integrity check and compares the schema with the one
// this program expects. It writes nothing.
func (s *Store) Check(ctx context.Context) (CheckReport, error) {
	var rep CheckReport
	if s.dialect == dialect.SQLite {
		lines, err := s.pragmaLines(ctx, "integrity_check")
		if err != nil {
			return rep, err
		}
		rep.Integrity = strings.Join(lines, "; ")
	} else {
		rep.Integrity = "ok"
	}
	p, err := s.computePlan(ctx)
	if err != nil {
		rep.Problems = append(rep.Problems, err.Error())
		return rep, nil
	}
	for _, tb := range p.tables {
		rep.Problems = append(rep.Problems, "missing table "+tb.Name)
	}
	for _, ac := range p.columns {
		rep.Problems = append(rep.Problems, fmt.Sprintf("missing column %s.%s", ac.Table.Name, ac.Column.Name))
	}
	for _, ai := range p.indexes {
		rep.Problems = append(rep.Problems, "missing index "+ai.Index.Name)
	}
	return rep, nil
}

// pragmaLines returns the first lines a check pragma yields; "ok" means healthy.
func (s *Store) pragmaLines(ctx context.Context, pragma string) ([]string, error) {
	var lines []string
	if err := s.on(ctx).Select(&lines, `PRAGMA `+pragma); err != nil {
		return nil, err
	}
	if len(lines) > 10 {
		lines = lines[:10]
	}
	return lines, nil
}

// HasConfiguration reports whether the database holds connections, keys,
// nodes, combos, kv rows or settings other than a password hash. A database
// without the tables counts as empty.
func (s *Store) HasConfiguration(ctx context.Context) (bool, error) {
	return s.hasConfiguration(s.on(ctx))
}

// HasConfigurationTx is HasConfiguration inside a transaction.
func (s *Store) HasConfigurationTx(tx contractsdb.Tx) (bool, error) {
	return s.hasConfiguration(tx)
}

func (s *Store) hasConfiguration(x contractsdb.Tx) (bool, error) {
	for _, table := range []string{"providerConnections", "apiKeys", "providerNodes", "combos", "kv"} {
		if !s.hasTable(table) {
			continue
		}
		query := `SELECT count(*) FROM ` + quote(table)
		var args []any
		if table == "kv" {
			query += ` WHERE "scope" <> ?`
			args = append(args, cryptScope)
		}
		n, err := s.count(x, query, args...)
		if err != nil {
			return false, err
		}
		if n > 0 {
			return true, nil
		}
	}
	if !s.hasTable("settings") {
		return false, nil
	}
	var raw []string
	if err := s.sel(x, &raw, `SELECT "data" FROM "settings" WHERE "id" = 1`); err != nil {
		return false, err
	}
	if len(raw) == 0 {
		return false, nil
	}
	d, err := document.ParseDoc(raw[0])
	if err != nil {
		return true, nil
	}
	for _, k := range d.Keys() {
		if k != "password" {
			return true, nil
		}
	}
	return false, nil
}

// ExportPlain writes a copy under dir with every secret opened and the sealing
// bookkeeping removed, so the previous program can read it. The copy holds
// plain credentials; a value that does not open aborts the export and removes
// it. The caller has already asked for the dashboard password.
func (s *Store) ExportPlain(ctx context.Context, dir string) (string, error) {
	if s.dialect != dialect.SQLite {
		return "", ErrExportUnsupported
	}
	if s.open == nil {
		return "", errors.New("repository: no way to open the copy is configured")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := s.backup(ctx, dir, "plain")
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		if rerr := os.RemoveAll(filepath.Dir(file)); rerr != nil {
			err = errors.Join(err, fmt.Errorf("repository: the plain copy could not be removed: %w", rerr))
		}
		return "", err
	}
	c, err := s.open(file, "_txlock=immediate&"+dialect.BusyWait)
	if err != nil {
		return fail(err)
	}
	cp := &Store{db: c.DB, schema: c.Schema, log: s.log, box: s.box, dialect: dialect.SQLite, version: s.version}
	err = cp.Tx(ctx, func(tx contractsdb.Tx) error {
		if err := cp.unsealAll(tx); err != nil {
			return err
		}
		return cp.stmt(tx, `DELETE FROM "kv" WHERE "scope" = ?`, cryptScope)
	})
	if err == nil {
		err = cp.Checkpoint(ctx)
	}
	if cerr := c.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	return file, nil
}
