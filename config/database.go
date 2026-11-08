package config

import (
	"path/filepath"
	"rllmx/app/dialect"

	"github.com/goravel/framework/contracts/database/driver"
	postgresfacades "github.com/goravel/postgres/facades"
	sqlitefacades "github.com/goravel/sqlite/facades"

	"rllmx/app/facades"
	"rllmx/app/file"
)

// sqliteDSN builds a file URI for a writable database with the settings the
// driver does not apply itself: a busy wait, WAL, foreign keys on, deleted
// pages overwritten, and immediate write transactions. extra pragmas run
// before the journal mode is set, because auto_vacuum only takes effect on a
// file that has no tables yet. The path is escaped because the project folder
// may contain spaces.
func sqliteDSN(path string, extra ...string) string {
	dsn := file.URI(path) + "?_txlock=immediate&" + dialect.BusyWait
	for _, p := range extra {
		dsn += "&_pragma=" + p
	}
	return dsn +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=secure_delete(ON)" +
		"&_pragma=temp_store(MEMORY)"
}

// sqliteReadOnlyDSN opens an existing file for reading only: a missing file is
// an error, not a new empty database, and a write fails in SQLite itself. The
// journal mode is left alone, because setting it is a write.
func sqliteReadOnlyDSN(path string) string {
	return file.URI(path) + "?mode=ro&" + dialect.BusyWait + "&_pragma=query_only(1)"
}

// sqliteBackupDSN is the read-only open a backup copy is made from. It differs
// from sqliteReadOnlyDSN by leaving query_only off: SQLite refuses VACUUM INTO
// under it, while mode=ro alone already stops every write to the source.
func sqliteBackupDSN(path string) string {
	return file.URI(path) + "?mode=ro&" + dialect.BusyWait
}

// envPath reads a variable and falls back to def when it is unset or empty.
func envPath(name, def string) string {
	if v, ok := facades.Config().Env(name, def).(string); ok && v != "" {
		return v
	}
	return def
}

func init() {
	config := facades.Config()

	dir := dataDir()
	sqliteMain := databaseFile("sqlite", dir)
	sqliteAnalytics := envPath("DB_DATABASE_ANALYTICS", filepath.Join(dir, "db", "analytics.sqlite"))
	pgMain := envPath("DB_DATABASE", "")
	pgAnalytics := envPath("DB_DATABASE_ANALYTICS", pgMain)

	postgres := func(name, database, schema string) map[string]any {
		return map[string]any{
			"host":     config.Env("DB_HOST", ""),
			"port":     config.Env("DB_PORT", 5432),
			"database": database,
			"username": config.Env("DB_USERNAME", ""),
			"password": config.Env("DB_PASSWORD", ""),
			"sslmode":  config.Env("DB_SSLMODE", "require"),
			"singular": false,
			"prefix":   "",
			"schema":   schema,
			"via": func() (driver.Driver, error) {
				return postgresfacades.Postgres(name)
			},
		}
	}

	config.Add("database", map[string]any{
		// DB_CONNECTION is the engine: "sqlite" (default, the 9router file) or
		// "postgres". The usage rows use the connection of the same engine with
		// the "_analytics" suffix.
		"default": config.Env("DB_CONNECTION", "sqlite"),
		"connections": map[string]any{
			"sqlite": map[string]any{
				"dsn":      sqliteDSN(sqliteMain),
				"database": sqliteMain,
				"prefix":   "",
				"singular": false,
				"via": func() (driver.Driver, error) {
					return sqlitefacades.Sqlite("sqlite")
				},
			},
			"sqlite_ro": map[string]any{
				"dsn":      sqliteReadOnlyDSN(sqliteMain),
				"database": sqliteMain,
				"prefix":   "",
				"singular": false,
				"via": func() (driver.Driver, error) {
					return sqlitefacades.Sqlite("sqlite_ro")
				},
			},
			"sqlite_backup": map[string]any{
				"dsn":      sqliteBackupDSN(sqliteMain),
				"database": sqliteMain,
				"prefix":   "",
				"singular": false,
				"via": func() (driver.Driver, error) {
					return sqlitefacades.Sqlite("sqlite_backup")
				},
			},
			"sqlite_analytics": map[string]any{
				"dsn":      sqliteDSN(sqliteAnalytics, "auto_vacuum(2)"),
				"database": sqliteAnalytics,
				"prefix":   "",
				"singular": false,
				"via": func() (driver.Driver, error) {
					return sqlitefacades.Sqlite("sqlite_analytics")
				},
			},
			"postgres":           postgres("postgres", pgMain, envPath("DB_SCHEMA", "public")),
			"postgres_analytics": postgres("postgres_analytics", pgAnalytics, "rllmx_analytics"),
		},
		// One pool setting serves every connection. A few connections let a
		// read run while a write transaction holds its own; the immediate
		// transaction lock and the busy timeout order the SQLite writers.
		"pool": map[string]any{
			"max_idle_conns":    4,
			"max_open_conns":    4,
			"conn_max_idletime": 3600,
			"conn_max_lifetime": 3600,
		},
		// Slow query threshold in milliseconds; a slower query is logged.
		"slow_threshold": 200,
		// Migration repository table. It is our own table, never a 9router one.
		"migrations": map[string]any{
			"table": "migrations",
		},
		// Sealing key for stored credentials: 64 hex characters. It is checked
		// when the store opens, not at boot, so version and health work without
		// it. Never printed.
		"encryption_secret": config.Env("DB_ENCRYPTION_SECRET", ""),
		// Usage rows older than this are pruned; the daily rollups stay.
		"analytics": map[string]any{
			"retention_days": config.Env("ANALYTICS_RETENTION_DAYS", 90),
		},
		// Redis connections
		"redis": map[string]any{
			"default": map[string]any{
				"host":     config.Env("REDIS_HOST", ""),
				"password": config.Env("REDIS_PASSWORD", ""),
				"port":     config.Env("REDIS_PORT", 6379),
				"database": config.Env("REDIS_DB", 0),
			},
		},
	})
}
