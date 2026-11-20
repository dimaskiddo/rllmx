package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/platform/settings"
	"rllmx/app/store/data"
	"rllmx/app/store/importer"
	"rllmx/app/store/repository"
)

// DBImportConfig seeds an empty database from a legacy one.
type DBImportConfig struct{ R *DatabaseRuntime }

func (c *DBImportConfig) Signature() string { return "db:import-config" }

func (c *DBImportConfig) Description() string {
	return "Seed an empty database from a legacy one; usage, the dashboard password and the login policy stay behind"
}

func (c *DBImportConfig) Extend() command.Extend {
	return command.Extend{Category: "db", Flags: []command.Flag{
		&command.StringFlag{Name: "from", Usage: "legacy database file or its data directory (required)"},
		&command.BoolFlag{Name: "dry-run", Usage: "show what would be copied and write nothing"},
		&command.BoolFlag{Name: "yes", Usage: "do not ask for confirmation"},
		&command.BoolFlag{Name: "with-sso", Usage: "also copy the login policy (authMode, OIDC and SAML settings)"},
		&command.StringFlag{Name: "skills-from", Usage: "also copy skill folders from a local folder or an https repository"},
	}}
}

func (c *DBImportConfig) Handle(ctx console.Context) error {
	from := ctx.Option("from")
	if from == "" {
		return errors.New("--from is required: a legacy database file or its data directory")
	}
	fc := c.R.Facades()
	if fc.Config == nil || fc.Opener == nil {
		return errors.New("db: the command is not configured")
	}
	cfg, err := settings.LoadDatabase(fc.Config)
	if err != nil {
		return err
	}
	src, err := repository.ResolveSourceDatabase(from)
	if err != nil {
		return err
	}
	// The lock is held for the whole command, a dry run included, so a server
	// cannot start against the same data directory while the plan is read.
	return c.R.withLock(cfg.DataDir, func() error {
		cleanup := takeWorkDir(cfg.DataDir)
		defer cleanup()
		if from := strings.TrimSpace(ctx.Option("skills-from")); from != "" {
			if err := c.copySkills(ctx, cfg, from); err != nil {
				return err
			}
		}
		plan, err := c.plan(ctx, fc, src, cfg.DataDir)
		if err != nil {
			return err
		}
		return c.run(ctx, fc, plan, cfg)
	})
}

// copySkills copies the folders named by --skills-from. A remote source needs
// the operator's allowlist, which is read from an existing database.
func (c *DBImportConfig) copySkills(ctx console.Context, cfg *settings.Store, src string) error {
	var sources []string
	if isRemote(src) {
		h, err := c.R.Open(ctx, data.ModeRead)
		if err != nil {
			return err
		}
		if sources, err = configuredSources(ctx, h); err != nil {
			return err
		}
	}
	return importSkills(ctx, cfg.DataDir, src, "main", sources, ctx.OptionBool("dry-run"))
}

// plan reads the source through a private copy and returns the plan. The copy
// is gone before the destination is opened.
func (c *DBImportConfig) plan(ctx console.Context, fc Facades, src, dataDir string) (*importer.Plan, error) {
	snap, err := repository.OpenSnapshot(fc.Opener, src, ImportWorkDir(dataDir))
	if err != nil {
		return nil, err
	}
	p, err := importer.Read(ctx, snap.DB(), importer.Options{WithSSO: ctx.OptionBool("with-sso")})
	cerr := snap.Close()
	if err != nil {
		return nil, err
	}
	if cerr != nil {
		return nil, cerr
	}
	ctx.Line(p.Report())
	return p, nil
}

// run checks the destination and applies the plan, or stops after the check
// for a dry run.
func (c *DBImportConfig) run(ctx console.Context, fc Facades, plan *importer.Plan, cfg *settings.Store) error {
	if ctx.OptionBool("dry-run") {
		if err := c.checkDestination(ctx, fc, cfg); err != nil {
			return err
		}
		ctx.Line("dry run: nothing was written")
		return nil
	}
	if err := c.R.confirm(ctx, "import-config", "Import this into the destination database? [y/N]"); err != nil {
		return err
	}
	h, err := c.R.Open(ctx, data.ModeWrite)
	if err != nil {
		return err
	}
	got, err := importer.Apply(ctx, h.Store, plan)
	if err != nil {
		return err
	}
	if got != plan.Counts() {
		return fmt.Errorf("import wrote %d connections, %d API keys, %d nodes, %d combos, %d kv rows, %d settings but the plan held more",
			got.Connections, got.APIKeys, got.Nodes, got.Combos, got.KV(), got.Settings)
	}
	ctx.Line(fmt.Sprintf("verified: wrote %d connections, %d API keys, %d nodes, %d combos, %d kv rows, %d settings",
		got.Connections, got.APIKeys, got.Nodes, got.Combos, got.KV(), got.Settings))
	return nil
}

// checkDestination refuses a destination that already holds configuration,
// without creating one: a missing SQLite file is an empty destination.
func (c *DBImportConfig) checkDestination(ctx console.Context, fc Facades, cfg *settings.Store) error {
	if cfg.Engine == settings.EngineSQLite {
		if err := requireFile(cfg.File); err != nil {
			return nil
		}
	}
	name := data.Connection(cfg.Engine, data.ModeRead)
	db, schema := fc.Connect(name)
	st, err := data.NewStore(data.Deps{DB: db, Schema: schema, Log: fc.Log}, cfg)
	if err != nil {
		return err
	}
	populated, err := st.HasConfiguration(ctx)
	if err != nil {
		return err
	}
	if populated {
		return importer.ErrDestinationNotEmpty
	}
	return nil
}
