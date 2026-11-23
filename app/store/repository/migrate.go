package repository

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	"rllmx/app/store/dialect"
)

const (
	schemaVersion       = "1"
	backupSchemaVersion = 2
	keptBackups         = 3

	// schemaChangePrefix names the backups the program takes itself before a
	// schema change; only those are ever pruned.
	schemaChangePrefix = "schema-change-"
	// preEncryptionLabel names the copy taken before secrets are sealed. It is
	// the untouched original, so it is never pruned.
	preEncryptionLabel = "pre-encryption"

	dbFileName = "data.sqlite"
)

type addColumnStep struct {
	Table  Table
	Column Column
}

type addIndexStep struct {
	Table Table
	Index Index
}

// plan is what is missing from the database, found before anything is written.
type plan struct {
	tables  []Table
	columns []addColumnStep
	indexes []addIndexStep
}

func (p plan) empty() bool { return len(p.tables) == 0 && len(p.columns) == 0 && len(p.indexes) == 0 }

type catalog struct {
	tables  map[string]bool
	indexes map[string]bool
	columns map[string]map[string]bool
}

// readCatalog reads what exists through the Schema facade. HasTable and its
// kin answer false on a query error, so the connection is asked first and a
// failure is an error, never "missing".
func (s *Store) readCatalog(ctx context.Context) (catalog, error) {
	var one []int64
	if err := s.on(ctx).Select(&one, "SELECT 1"); err != nil {
		return catalog{}, fmt.Errorf("repository: the database does not answer: %w", err)
	}
	c := catalog{tables: map[string]bool{}, indexes: map[string]bool{}, columns: map[string]map[string]bool{}}
	for _, tb := range tables {
		if !s.hasTable(tb.Name) {
			continue
		}
		c.tables[tb.Name] = true
		cols, err := s.ColumnNames(ctx, tb.Name)
		if err != nil {
			return c, fmt.Errorf("repository: read columns of %s: %w", tb.Name, err)
		}
		c.columns[tb.Name] = map[string]bool{}
		for _, col := range cols {
			c.columns[tb.Name][col] = true
		}
		idx, err := s.schema.GetIndexes(tb.Name)
		if err != nil {
			return c, fmt.Errorf("repository: read indexes of %s: %w", tb.Name, err)
		}
		for _, ix := range idx {
			c.indexes[ix.Name] = true
		}
	}
	return c, nil
}

// hasTable checks one table. On PostgreSQL the name is qualified with the
// schema, because HasTable looks in every schema.
func (s *Store) hasTable(name string) bool {
	if s.dialect == dialect.Postgres && s.pgSchema != "" {
		return s.schema.HasTable(s.pgSchema + "." + name)
	}
	return s.schema.HasTable(name)
}

// NeedsMigration reports whether the database is missing tables, columns or
// indexes this program expects. It writes nothing and needs no secret.
func (s *Store) NeedsMigration(ctx context.Context) (bool, error) {
	p, err := s.computePlan(ctx)
	if err != nil {
		return false, err
	}
	return !p.empty(), nil
}

func (s *Store) computePlan(ctx context.Context) (plan, error) {
	cat, err := s.readCatalog(ctx)
	if err != nil {
		return plan{}, err
	}
	var p plan
	for _, tb := range tables {
		if !cat.tables[tb.Name] {
			p.tables = append(p.tables, tb)
		} else {
			for _, c := range tb.Columns {
				if cat.columns[tb.Name][c.Name] {
					continue
				}
				if c.PK || (c.NotNull && c.Default == "") || c.Unique {
					return plan{}, fmt.Errorf("repository: table %s lacks column %s, which cannot be added to an existing table", tb.Name, c.Name)
				}
				p.columns = append(p.columns, addColumnStep{tb, c})
			}
		}
		for _, ix := range tb.Indexes {
			if !cat.indexes[ix.Name] {
				p.indexes = append(p.indexes, addIndexStep{tb, ix})
			}
		}
	}
	return p, nil
}

// inspect reports whether the database is new (no table besides an empty or
// missing _meta) and whether _meta needs seeding, before anything is written.
func (s *Store) inspect(ctx context.Context) (fresh, seedMeta bool, err error) {
	cat, err := s.readCatalog(ctx)
	if err != nil {
		return false, false, err
	}
	seedMeta = !cat.tables["_meta"]
	if !seedMeta {
		n, err := s.count(s.on(ctx), `SELECT count(*) FROM "_meta"`)
		if err != nil {
			return false, false, err
		}
		seedMeta = n == 0
	}
	if !seedMeta {
		return false, false, nil
	}
	for _, tb := range tables {
		if tb.Name != "_meta" && cat.tables[tb.Name] {
			return false, true, nil
		}
	}
	return true, true, nil
}

// seedMeta is idempotent: on PostgreSQL two processes can both find a new
// database, and the second must not fail on the rows the first wrote.
func (s *Store) seedMeta(tx contractsdb.Tx) error {
	for _, kv := range [][2]string{
		{"schemaVersion", schemaVersion},
		{"backupSchemaVersion", strconv.Itoa(backupSchemaVersion)},
		{"appVersion", "rllmx-" + s.version},
		{"migratedAt", dialect.Now()},
	} {
		if err := s.stmt(tx, `INSERT INTO "_meta"("key","value") VALUES (?,?) ON CONFLICT("key") DO NOTHING`, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// Migrate brings the database to the schema this program expects and seals its
// secrets. The plan is made first with reads only: when nothing is missing and
// the check row is present, nothing is written and no backup is taken.
// Otherwise one backup is made (a failed one aborts), the framework migrator
// creates what is missing, and a second transaction seals plain secrets and
// writes the check row; that second step can be repeated, so a start that
// stopped between the two finishes the work. The warnings never hold a value.
func (s *Store) Migrate(ctx context.Context) ([]string, error) {
	if _, err := s.sealer(); err != nil {
		return nil, err
	}
	fresh, seedMeta, err := s.inspect(ctx)
	if err != nil {
		return nil, fmt.Errorf("repository: inspect database: %w", err)
	}
	p, err := s.computePlan(ctx)
	if err != nil {
		return nil, err
	}

	if fresh {
		if err := s.runMigrator(ctx); err != nil {
			return nil, err
		}
		return nil, s.Tx(ctx, func(tx contractsdb.Tx) error {
			if err := s.seedMeta(tx); err != nil {
				return err
			}
			return s.writeCheck(tx)
		})
	}

	var warns []string
	if !seedMeta {
		stored, err := s.metaValue(ctx, "backupSchemaVersion")
		if err != nil {
			return nil, fmt.Errorf("repository: read _meta: %w", err)
		}
		if v, perr := strconv.Atoi(stored); perr == nil && v > backupSchemaVersion {
			warns = append(warns, fmt.Sprintf("database schema version %d is newer than this program knows (%d); only known tables and columns are used", v, backupSchemaVersion))
		}
	}

	hasCheck, err := s.checkState(s.on(ctx))
	if err != nil {
		return nil, err
	}
	sp, err := s.scanSecrets(s.on(ctx), false)
	if err != nil {
		return nil, fmt.Errorf("repository: scan secrets: %w", err)
	}
	if !hasCheck && sp.unopenable > 0 {
		return nil, ErrWrongEncryptionSecret
	}
	if sp.skipped > 0 {
		warns = append(warns, fmt.Sprintf("%d rows hold damaged JSON; their secrets were left as they are", sp.skipped))
	}

	changesData := !p.empty() || seedMeta || sp.values() > 0
	if !changesData && hasCheck {
		return warns, nil
	}
	if changesData {
		label := strings.TrimSuffix(schemaChangePrefix, "-")
		if sp.values() > 0 {
			label = preEncryptionLabel
		}
		w, err := s.backupBeforeChange(ctx, label)
		if err != nil {
			return nil, err
		}
		warns = append(warns, w...)
	}
	if !p.empty() {
		if err := s.runMigrator(ctx); err != nil {
			return nil, err
		}
		if err := s.repair(ctx); err != nil {
			return nil, err
		}
	}

	var done sealPlan
	err = s.Tx(ctx, func(tx contractsdb.Tx) error {
		if seedMeta {
			if err := s.seedMeta(tx); err != nil {
				return err
			}
		}
		var err error
		if done, err = s.scanSecrets(tx, true); err != nil {
			return err
		}
		return s.writeCheck(tx)
	})
	if err != nil {
		return nil, err
	}
	if done.values() > 0 && s.dialect == dialect.SQLite {
		// Plain copies stay in free pages and the WAL until the file is rebuilt.
		if err := s.Vacuum(ctx); err != nil {
			return nil, fmt.Errorf("repository: rebuild the database after sealing: %w", err)
		}
		if err := s.Checkpoint(ctx); err != nil {
			return nil, fmt.Errorf("repository: checkpoint after sealing: %w", err)
		}
	}
	if done.values() > 0 {
		s.log.Infof("secrets sealed: %d client keys, %d connection fields, %d setting fields", done.keys, done.connFields, done.settingFields)
		if n := s.legacyUsageKeys(ctx); n > 0 {
			warns = append(warns, fmt.Sprintf("%d old usage rows still hold client keys in plain text; `rllmx artisan db:import-analytics --drop-old` moves them to the analytics store without the key", n))
		}
	}
	return warns, nil
}

func (s *Store) runMigrator(ctx context.Context) error {
	if s.migrate == nil {
		return fmt.Errorf("repository: the database needs a schema change and no migrator is configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.migrate(); err != nil {
		return fmt.Errorf("repository: apply migrations: %w", err)
	}
	return nil
}

func (s *Store) metaValue(ctx context.Context, key string) (string, error) {
	var v []string
	if err := s.sel(s.on(ctx), &v, `SELECT "value" FROM "_meta" WHERE "key" = ?`, key); err != nil {
		return "", err
	}
	if len(v) == 0 {
		return "", nil
	}
	return v[0], nil
}

// backupBeforeChange takes the one backup that precedes a change. On dialect.SQLite a
// failed backup aborts the change; PostgreSQL has no file to copy, so it only
// warns.
func (s *Store) backupBeforeChange(ctx context.Context, label string) ([]string, error) {
	if s.dialect != dialect.SQLite {
		return []string{"the PostgreSQL database is being changed without an automatic backup; use pg_dump"}, nil
	}
	dest, err := s.backup(ctx, s.backupDir, label)
	if err != nil {
		return nil, fmt.Errorf("repository: backup before the change failed, nothing was changed: %w", err)
	}
	s.log.Infof("database backed up before a change: %s", filepath.Base(filepath.Dir(dest)))
	if err := pruneBackups(s.backupDir, keptBackups); err != nil {
		s.log.Warningf("could not prune old backups: %v", err)
	}
	return nil, nil
}

// backup copies the dialect.SQLite database into a new timestamped directory under dir
// with VACUUM INTO. The copy is made with the process umask, so it is
// restricted to its owner afterwards.
func (s *Store) backup(ctx context.Context, dir, label string) (string, error) {
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-rllmx-%s-%s-%x", label, s.version, time.Now().UTC().Format("20060102-150405.000"), suffix)
	target := filepath.Join(dir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return "", err
	}
	file := filepath.Join(target, dbFileName)
	if err := s.on(ctx).Statement(`VACUUM INTO ?`, file); err != nil {
		_ = os.RemoveAll(target)
		return "", err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(file, 0o600); err != nil {
			_ = os.RemoveAll(target)
			return "", err
		}
	}
	return file, nil
}

// pruneBackups keeps the newest keep schema-change directories (by
// modification time). Manual and other backups are never touched.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type dirInfo struct {
		name string
		mod  time.Time
	}
	var dirs []dirInfo
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), schemaChangePrefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		dirs = append(dirs, dirInfo{e.Name(), info.ModTime()})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].mod.Before(dirs[j].mod) })
	for len(dirs) > keep {
		if err := os.RemoveAll(filepath.Join(dir, dirs[0].name)); err != nil {
			return err
		}
		dirs = dirs[1:]
	}
	return nil
}

// ColumnNames lists a table's columns in declaration order, through a query on
// the DB facade. The Schema facade's own column reader scans the primary-key
// position into a bool and fails on a table whose key has two columns, which kv
// has.
func (s *Store) ColumnNames(ctx context.Context, table string) ([]string, error) {
	query := `SELECT name FROM pragma_table_info(?)`
	if s.dialect == dialect.Postgres {
		query = `SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ? ORDER BY ordinal_position`
	}
	var names []string
	if err := s.sel(s.on(ctx), &names, query, table); err != nil {
		return nil, err
	}
	return names, nil
}

// repair applies what is still missing after the migrator ran. The framework
// logs a migration once and never runs it again, so an index or column dropped
// from a database it already migrated is not brought back by it. The same DDL
// the migrations use is applied here, from the plan, and nothing else.
func (s *Store) repair(ctx context.Context) error {
	p, err := s.computePlan(ctx)
	if err != nil {
		return err
	}
	// The plan covers the compatibility set, because a real 9router database
	// must satisfy it as it is. Our own tables are created unconditionally;
	// CREATE TABLE IF NOT EXISTS makes that a no-op on a database that has them.
	for _, tb := range ownTables {
		if err := s.schema.Sql(createTableSQL(tb, s.dialect)); err != nil {
			return fmt.Errorf("repository: create table %s: %w", tb.Name, err)
		}
		for _, ix := range tb.Indexes {
			if err := s.schema.Sql(createIndexSQL(tb, ix)); err != nil {
				return fmt.Errorf("repository: create index %s: %w", ix.Name, err)
			}
		}
	}
	for _, tb := range p.tables {
		if err := s.schema.Sql(createTableSQL(tb, s.dialect)); err != nil {
			return fmt.Errorf("repository: create table %s: %w", tb.Name, err)
		}
	}
	for _, ac := range p.columns {
		if err := s.schema.Sql(addColumnSQL(ac.Table, ac.Column, s.dialect)); err != nil {
			return fmt.Errorf("repository: add column %s.%s: %w", ac.Table.Name, ac.Column.Name, err)
		}
	}
	for _, ai := range p.indexes {
		if err := s.schema.Sql(createIndexSQL(ai.Table, ai.Index)); err != nil {
			return fmt.Errorf("repository: create index %s: %w", ai.Index.Name, err)
		}
	}
	return nil
}
