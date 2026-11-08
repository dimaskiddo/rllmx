package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/goravel/framework/contracts/config"
	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/contracts/log"
	"golang.org/x/term"

	"rllmx/app/data"
	"rllmx/app/repository"
	"rllmx/app/settings"
)

// Facades are the framework pieces a db command opens the database with, as
// interfaces. The real ones come from bootstrap; a test hands in fakes.
type Facades struct {
	Config config.Config
	Log    log.Log
	// Connect returns the DB and Schema facades bound to a named connection from
	// config/database.go.
	Connect func(name string) (contractsdb.DB, contractsschema.Schema)
	// Opener opens one SQLite file under its own connection name, for a legacy
	// source copy. The provider supplies it; a test may leave it nil.
	Opener repository.Opener
}

// NewDatabaseRuntime is the runtime the registered db commands use. Open reads
// the settings, picks the connection for the mode, and builds a store that has
// no migrator: a command never changes the schema, so a database that needs a
// change is reported as it is.
func NewDatabaseRuntime(f func() Facades, version string) *DatabaseRuntime {
	return &DatabaseRuntime{
		Open: func(ctx context.Context, m data.Mode) (*Handle, error) {
			fc := f()
			cfg, err := settings.LoadDatabase(fc.Config)
			if err != nil {
				return nil, err
			}
			name := data.Connection(cfg.Engine, m)
			if cfg.Engine == settings.EngineSQLite {
				if err := requireFile(cfg.File); err != nil {
					return nil, err
				}
			}
			db, schema := fc.Connect(name)
			store, err := data.NewStore(data.Deps{DB: db, Schema: schema, Log: fc.Log, Version: version, Open: fc.Opener}, cfg)
			if err != nil {
				return nil, err
			}
			return &Handle{Store: store, DataDir: cfg.DataDir}, nil
		},
		Analytics: func(ctx context.Context) (*AnalyticsHandle, error) {
			fc := f()
			cfg, err := settings.LoadDatabase(fc.Config)
			if err != nil {
				return nil, err
			}
			h := &AnalyticsHandle{DataDir: cfg.DataDir, Source: cfg.File, Log: fc.Log, Opener: fc.Opener}
			if cfg.Engine == settings.EngineSQLite {
				if err := requireFile(cfg.File); err != nil {
					return nil, err
				}
				// A plain source needs no key opener, and a wrong secret must be
				// reported as that rather than as a key per row.
				keys, err := openMainStore(fc, cfg, version)
				if err != nil {
					return nil, err
				}
				if err := keys.Verify(ctx); err != nil {
					return nil, err
				}
				h.Keys = keys
			}
			usage, err := data.OpenAnalytics(ctx, cfg, data.AnalyticsDeps{
				Config: fc.Config, Connect: fc.Connect, Log: fc.Log, Open: fc.Opener, Version: version,
			})
			if err != nil {
				return nil, err
			}
			h.Store = usage
			return h, nil
		},
		Terminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		Facades:  f,
	}
}

// openMainStore builds the configuration store over the read-only connection,
// the one a command consults for a sealed key.
func openMainStore(fc Facades, cfg *settings.Store, version string) (*repository.Store, error) {
	name := data.Connection(cfg.Engine, data.ModeRead)
	db, schema := fc.Connect(name)
	return data.NewStore(data.Deps{DB: db, Schema: schema, Log: fc.Log, Version: version}, cfg)
}

// ImportWorkDir is where a command copies a legacy source. The copy is removed
// whatever the outcome.
func ImportWorkDir(dataDir string) string { return filepath.Join(dataDir, "db", "import-tmp") }

// takeWorkDir makes the copy folder and returns a function that removes it when
// this command created it. A folder that already existed is left alone, and a
// folder that still holds something is not empty, so the remove fails harmlessly.
func takeWorkDir(dataDir string) func() {
	dir := ImportWorkDir(dataDir)
	_, statErr := os.Stat(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	created := os.IsNotExist(statErr)
	return func() {
		if created {
			_ = os.Remove(dir)
		}
	}
}

// requireFile refuses a missing database file. The read-only connections never
// create one, and the writer would: a command must not make a database.
func requireFile(path string) error {
	if path == "" {
		return errors.New("db: no database file is configured")
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return errors.New("db: there is no database at the configured path; start the server once to create it")
	}
	if info.Size() == 0 {
		return errors.New("db: the database file is empty; start the server once to create its tables")
	}
	return nil
}
