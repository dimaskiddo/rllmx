package data

import (
	"context"
	"path/filepath"
	"rllmx/app/store/dialect"

	"github.com/goravel/framework/contracts/config"
	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/platform/settings"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
	"rllmx/app/store/sealer"
)

// Mode says what a caller may do to the database, which decides the connection.
type Mode int

const (
	// ModeWrite is the writer: the server and the commands that change data.
	ModeWrite Mode = iota
	// ModeRead opens a SQLite file read-only with query_only, for a command
	// that must not write.
	ModeRead
	// ModeBackup opens a SQLite file read-only without query_only, because
	// SQLite refuses VACUUM INTO under it.
	ModeBackup
	// ModeInit is the writer that may open the empty file a start-up leaves
	// behind, for the command that creates the schema before a server has run.
	ModeInit
)

// Connection names the Goravel connection (config/database.go) for an engine
// and a mode. PostgreSQL has one connection for all of them.
func Connection(engine string, m Mode) string {
	if engine == settings.EnginePostgres {
		return "postgres"
	}
	switch m {
	case ModeRead:
		return "sqlite_ro"
	case ModeBackup:
		return "sqlite_backup"
	}
	return "sqlite"
}

// AnalyticsConnection names the connection of the usage store. It is always a
// writable one: the store is ours and is written on every request.
func AnalyticsConnection(engine string) string {
	if engine == settings.EnginePostgres {
		return "postgres_analytics"
	}
	return "sqlite_analytics"
}

// AnalyticsFile is the SQLite file of the usage store, empty on PostgreSQL.
func AnalyticsFile(cfg config.Config, engine string) string {
	if engine == settings.EnginePostgres {
		return ""
	}
	return cfg.GetString("database.connections.sqlite_analytics.database")
}

// DialectOf is the dialect of the configured engine. The migrations are declared
// before the settings are checked, so an engine that names neither falls back
// to SQLite and config.Boot refuses it a moment later.
func DialectOf(cfg config.Config) dialect.Dialect {
	if cfg.GetString("database.default", settings.EngineSQLite) == settings.EnginePostgres {
		return dialect.Postgres
	}
	return dialect.SQLite
}

// BackupDir is where the automatic and the default manual backups go.
func BackupDir(dataDir string) string { return filepath.Join(dataDir, "db", "backups") }

// Deps are the facades a store is built over, passed as their interfaces.
type Deps struct {
	DB     contractsdb.DB
	Schema contractsschema.Schema
	Log    log.Log
	// Migrate runs the framework migrator; only the server and db:migrate set it.
	Migrate func() error
	// Open opens another SQLite file; only ExportPlain needs it.
	Open repository.Opener
	// Version is written into backup names and a new database's _meta.
	Version string
}

// AnalyticsDeps are what the usage store needs. Connect names the analytics
// connection from config/database.go; Open opens the legacy source copies.
type AnalyticsDeps struct {
	Config config.Config
	// Connect returns the DB and Schema facades bound to a named connection.
	Connect func(name string) (contractsdb.DB, contractsschema.Schema)
	Log     log.Log
	Open    repository.Opener
	// Version is written into backup names.
	Version string
}

// OpenAnalytics connects the usage store, makes its SQLite file private before
// the driver can create it, and creates what is missing. The server and the db
// commands open it the same way, so a command sees the store the server sees.
func OpenAnalytics(ctx context.Context, s *settings.Store, d AnalyticsDeps) (*analytics.Store, error) {
	opts := analytics.Options{
		Log: d.Log, Dialect: DialectOf(d.Config), PGSchema: analytics.SchemaName,
		Version: d.Version, BackupDir: filepath.Join(s.DataDir, "db", "analytics-backups"),
		Open: d.Open,
	}
	if s.Engine == settings.EngineSQLite {
		opts.File = AnalyticsFile(d.Config, s.Engine)
		if err := analytics.Prepare(opts.File); err != nil {
			return nil, err
		}
	}
	conn := AnalyticsConnection(s.Engine)
	opts.DB, opts.Schema = d.Connect(conn)
	return analytics.Open(ctx, opts)
}

// NewStore builds a configuration store over deps. The secret is optional: a
// store without one can back up, check and vacuum but seals and opens nothing.
func NewStore(d Deps, cfg *settings.Store) (*repository.Store, error) {
	var box *sealer.Box
	if len(cfg.Secret) > 0 {
		b, err := sealer.New(cfg.Secret)
		if err != nil {
			return nil, err
		}
		box = b
	}
	engine := dialect.SQLite
	if cfg.Engine == settings.EnginePostgres {
		engine = dialect.Postgres
	}
	return repository.New(repository.Options{
		DB: d.DB, Schema: d.Schema, Log: d.Log, Box: box, Migrate: d.Migrate, Dialect: engine,
		PGSchema: cfg.PGSchema, BackupDir: BackupDir(cfg.DataDir), AppVersion: d.Version, Open: d.Open,
	})
}
