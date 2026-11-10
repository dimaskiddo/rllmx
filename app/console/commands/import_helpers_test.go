package commands

import (
	"context"
	"path/filepath"
	"rllmx/app/store/dialect"
	"testing"

	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"

	"rllmx/app/store/analytics"
	"rllmx/app/store/data"
	"rllmx/tests"
	"rllmx/tests/store"
)

// importConsole answers the prompts an import asks: the y/N confirmation and the
// typed DROP word.
type importConsole struct{ dbConsole }

// importRig is a data directory with a destination database, its own usage
// store and the legacy source an import reads.
type importRig struct {
	t      *testing.T
	dest   *store.Store
	usage  *analytics.Store
	dir    string
	tty    bool
	exited int
}

func newImportRig(t *testing.T) *importRig {
	t.Helper()
	dir := t.TempDir()
	dest := store.Over(t, tests.OpenSQLite(t, filepath.Join(dir, "data.sqlite")), tests.DBSecret)
	if _, err := dest.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &importRig{t: t, dest: dest, dir: dir, tty: true}
}

// usageStore opens the usage store over its own file, the way the server does.
func (r *importRig) usageStore(ctx context.Context) (*analytics.Store, error) {
	if r.usage != nil {
		return r.usage, nil
	}
	path := filepath.Join(r.dir, "db", "analytics.sqlite")
	if err := analytics.Prepare(path); err != nil {
		return nil, err
	}
	sq := tests.OpenSQLiteWithQuery(r.t, path, "_txlock=immediate&_pragma=busy_timeout(5000)")
	s, err := analytics.Open(ctx, analytics.Options{
		DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Dialect: dialect.SQLite,
		File: path, Version: "test", Open: store.Opener(r.t),
	})
	if err != nil {
		return nil, err
	}
	r.usage = s
	return s, nil
}

func (r *importRig) runtime() *DatabaseRuntime {
	return &DatabaseRuntime{
		Open: func(_ context.Context, _ data.Mode) (*Handle, error) {
			return &Handle{Store: r.dest.Store, DataDir: r.dir}, nil
		},
		Analytics: func(ctx context.Context) (*AnalyticsHandle, error) {
			usage, err := r.usageStore(ctx)
			if err != nil {
				return nil, err
			}
			return &AnalyticsHandle{
				Store: usage, Keys: r.dest.Store, DataDir: r.dir, Source: r.dest.SQ.File,
				Log: r.dest.SQ.Log, Opener: store.Opener(r.t),
			}, nil
		},
		Terminal: func() bool { return r.tty },
		Exit:     func(code int) { r.exited = code },
		Facades: func() Facades {
			return Facades{
				Config: tests.Config{Values: map[string]any{
					"database.default":                               "sqlite",
					"database.connections.sqlite.database":           r.dest.SQ.File,
					"database.connections.sqlite_analytics.database": filepath.Join(r.dir, "db", "analytics.sqlite"),
					"app.data_dir":                                   r.dir,
					"database.analytics.retention_days":              90,
					"database.encryption_secret":                     tests.DBSecret,
				}},
				Log:    r.dest.SQ.Log,
				Opener: store.Opener(r.t),
				Connect: func(string) (contractsdb.DB, contractsschema.Schema) {
					return r.dest.SQ.DB, r.dest.SQ.Schema
				},
			}
		},
	}
}
