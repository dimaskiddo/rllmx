package providers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goravel/framework/contracts/binding"
	contractsdb "github.com/goravel/framework/contracts/database/db"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	"github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/schedule"
	"github.com/goravel/framework/database/migration"

	"rllmx/app/analytics"
	"rllmx/app/auth"
	"rllmx/app/cache"
	"rllmx/app/data"
	"rllmx/app/identity"
	"rllmx/app/oidc"
	"rllmx/app/repository"
	"rllmx/app/settings"
)

// BindingData is the container key of the data pieces the data runner, the HTTP
// server and the maintenance schedule share.
const BindingData = "rllmx.data"

// drainTimeout bounds the last usage flush at shutdown.
const drainTimeout = 30 * time.Second

// Data is what the binding holds.
type Data struct {
	Settings *settings.Store
	// Gate is opened by the runner once the stores are ready; the HTTP server
	// waits on it.
	Gate   *data.Gate
	Runner *data.Runner
	Jobs   *data.JobSet
}

// DataOf returns the bound data pieces. A missing binding means the provider is
// not registered, which is a bug in the program, so it panics.
func DataOf(app foundation.Application) *Data {
	v, err := app.Make(BindingData)
	d, ok := v.(*Data)
	if err != nil || !ok || d == nil {
		panic(fmt.Sprintf("providers: %s is not bound: %v", BindingData, err))
	}
	return d
}

// ScheduleOf returns the maintenance events, for WithSchedule.
func ScheduleOf(app foundation.Application) []schedule.Event { return DataOf(app).Jobs.Events() }

// DataServiceProvider owns the data. Register binds, Boot does nothing, and the
// work (the lock, the stores, the maintenance run) is in the runner, so an
// Artisan command that boots this provider takes no lock and opens no store.
type DataServiceProvider struct {
	// Version is the build version, written into backup names and a new
	// database's _meta.
	Version string
}

// Relationship names what the provider binds and what it reads.
func (r *DataServiceProvider) Relationship() binding.Relationship {
	return binding.Relationship{
		Bindings:     []string{BindingData},
		Dependencies: []string{binding.Config, binding.Log, binding.DB, binding.Schema, binding.Schedule},
		ProvideFor:   []string{},
	}
}

// Register binds the settings, the gate, the runner and the job set. The
// settings are checked here, so a malformed DB_ENCRYPTION_SECRET stops the start
// before anything is opened. A missing one is refused later, when the runner
// opens the store: the framework resolves this binding for every command too,
// and version and health need no secret.
func (r *DataServiceProvider) Register(app foundation.Application) {
	app.Singleton(BindingData, func(app foundation.Application) (any, error) {
		cfg := app.MakeConfig()
		if cfg == nil {
			return nil, errors.New("providers: the config facade is not set")
		}
		s, err := settings.LoadDatabase(cfg)
		if err != nil {
			return nil, err
		}
		d := &Data{Settings: s, Gate: data.NewGate()}
		jobs := data.NewJobSet(append(
			data.MainJobs(func() *repository.Store { return d.Runner.Store() }),
			data.AnalyticsJobs(func() *analytics.Store { return d.Runner.Analytics() }, func() int { return s.RetentionDays })...,
		), app.Context(), app.MakeLog(), time.Now)
		d.Jobs = jobs
		d.Runner = data.NewRunner(data.Options{
			DataDir: s.DataDir, Gate: d.Gate, Log: app.MakeLog(), Context: app.Context(),
			Open:    openMain(app, s, r.Version),
			OnReady: jobs.RunStartup,
		})
		return d, nil
	})
}

// Boot has nothing to start; see the type comment.
func (r *DataServiceProvider) Boot(app foundation.Application) {}

// Runners returns the data runner.
func (r *DataServiceProvider) Runners(app foundation.Application) []foundation.Runner {
	return []foundation.Runner{DataOf(app).Runner}
}

// openMain connects the configuration database through the DB and Schema
// facades, migrates it and seals its secrets. Nothing before this call has
// written to the database. The usage store opens next: its schema is created by
// its own pass and its writer starts before the gate opens, so no request can
// find a store that is not there yet.
func openMain(app foundation.Application, s *settings.Store, version string) func(context.Context) (*data.Opened, error) {
	return func(ctx context.Context) (*data.Opened, error) {
		if err := s.RequireSecret(); err != nil {
			return nil, err
		}
		schema := app.MakeSchema()
		store, err := data.NewStore(data.Deps{
			DB: app.MakeDB().Connection(data.Connection(s.Engine, data.ModeWrite)), Schema: schema.Connection(data.Connection(s.Engine, data.ModeWrite)),
			Log: app.MakeLog(), Version: version,
			Migrate: func() error {
				return migration.NewMigrator(app.MakeArtisan(), schema, app.MakeConfig().GetString("database.migrations.table")).Run()
			},
		}, s)
		if err != nil {
			return nil, err
		}
		warnings, err := store.Migrate(ctx)
		if err != nil {
			return nil, err
		}
		authWarnings, err := authStartup(ctx, app, store)
		if err != nil {
			return nil, err
		}
		warnings = append(warnings, authWarnings...)
		onSettingsWrite(store, func() *cache.Cache { return cacheOrNil(app) })

		usage, err := openAnalytics(ctx, app, s, version)
		if err != nil {
			return nil, err
		}
		writer := usage.NewWriter(analytics.WriterOptions{})
		return &data.Opened{
			Store: store, Analytics: usage, Writer: writer, Warnings: warnings,
			// The writer goes first: it is still draining rows into the usage
			// store, so neither store may close under it. The drain is bounded,
			// because shutdown waits on this call.
			Close: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
				defer cancel()
				werr := writer.Close(ctx)
				aerr := usage.Close(ctx)
				cerr := store.Checkpoint(ctx)
				return errors.Join(werr, aerr, cerr)
			},
		}, nil
	}
}

// openAnalytics connects the usage store through the one open path the db
// commands use, so a command sees the file the server sees.
func openAnalytics(ctx context.Context, app foundation.Application, s *settings.Store, version string) (*analytics.Store, error) {
	cfg := app.MakeConfig()
	return data.OpenAnalytics(ctx, s, data.AnalyticsDeps{
		Config: cfg,
		Connect: func(name string) (contractsdb.DB, contractsschema.Schema) {
			return app.MakeDB().Connection(name), app.MakeSchema().Connection(name)
		},
		Log: app.MakeLog(), Open: SQLiteOpener(cfg, app.MakeLog()), Version: version,
	})
}

// authStartup runs the two auth checks the start owns once the store is open:
// it creates the machine id a key is bound to, and refuses the start while the
// dashboard password is still a public default when
// AUTH_REFUSE_DEFAULT_PASSWORD is set. The warnings never repeat a value.
func authStartup(ctx context.Context, app foundation.Application, store *repository.Store) ([]string, error) {
	cfg := app.MakeConfig()
	if cfg == nil {
		return nil, nil
	}
	dir := DataOf(app).Settings.DataDir
	var warnings []string

	if err := EnsureSessionSecret(cfg); err != nil {
		return nil, err
	}

	machine := identity.NewMachine(cfg.GetString("auth.machine_id_salt"))
	if _, err := machine.Ensure(dir); err != nil {
		return nil, err
	}
	if machine.DefaultSalt() {
		warnings = append(warnings, "AUTH_MACHINE_ID_SALT is the public default; set it to the value the previous install used, or keys created elsewhere read as a checksum mismatch")
	}
	if identity.NewKeys(cfg.GetString("auth.api_key_secret")).DefaultSecret() {
		warnings = append(warnings, "AUTH_API_KEY_SECRET is the public default; set it to the value the previous install used, or keys created elsewhere read as a checksum mismatch")
	}

	if !cfg.GetBool("auth.refuse_default_password") {
		return warnings, nil
	}
	pass := auth.NewPasswords(app.MakeHash(), cfg.GetString("auth.initial_password"))
	d, err := store.Settings().Effective(ctx)
	if err != nil {
		return nil, err
	}
	stored, _ := d.GetString("password")
	if pass.WellKnown(stored) {
		return nil, errors.New("providers: AUTH_REFUSE_DEFAULT_PASSWORD is set and the dashboard password is still a public default; change it with: rllmx artisan password:reset")
	}
	return warnings, nil
}

// purgeResponseCache drops the cached settings projection and every cached
// answer, after a settings write.
func purgeResponseCache(c *cache.Cache) {
	if c != nil {
		c.Purge(cache.Settings)
		c.Purge(cache.Response)
	}
}

// onSettingsWrite drops what is cached from the settings row when it changes:
// an identity provider's metadata, which a changed provider must not outlive,
// and the response cache's switch and answers.
func onSettingsWrite(store *repository.Store, c func() *cache.Cache) {
	store.OnSettingsChange(func() { oidc.Purge(c()) })
	store.OnSettingsChange(func() { purgeResponseCache(c()) })
}
