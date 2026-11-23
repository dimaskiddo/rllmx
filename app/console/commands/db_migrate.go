package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/store/data"
)

// DBMigrate creates or updates the schema and seals what the server expects.
type DBMigrate struct{ R *DatabaseRuntime }

func (c *DBMigrate) Signature() string { return "db:migrate" }

func (c *DBMigrate) Description() string {
	return "Create or update the database schema; the server must not be running"
}

func (c *DBMigrate) Extend() command.Extend { return command.Extend{Category: "db"} }

func (c *DBMigrate) Handle(ctx console.Context) error {
	h, err := c.R.Open(ctx, data.ModeInit)
	if err != nil {
		return err
	}
	return c.R.withLock(h.DataDir, func() error {
		warns, err := h.Store.Migrate(ctx)
		if err != nil {
			return err
		}
		for _, w := range warns {
			ctx.Line(w)
		}
		ctx.Line("schema is up to date")
		return nil
	})
}
