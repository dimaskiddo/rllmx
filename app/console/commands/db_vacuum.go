package commands

import (
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"
	"rllmx/app/data"
	"strings"
)

// DBVacuum rebuilds the database file to return free pages.
type DBVacuum struct{ R *DatabaseRuntime }

func (c *DBVacuum) Signature() string { return "db:vacuum" }

func (c *DBVacuum) Description() string {
	return "Rebuild the main database file to reclaim free space; the server must not be running"
}

func (c *DBVacuum) Extend() command.Extend {
	return command.Extend{Category: "db", Flags: []command.Flag{
		&command.BoolFlag{Name: "yes", Usage: "do not ask for confirmation"},
	}}
}

func (c *DBVacuum) Handle(ctx console.Context) error {
	if err := c.R.confirm(ctx, "vacuum", "Rebuild the database file? The server must not be running. [y/N]"); err != nil {
		return err
	}
	h, err := c.R.Open(ctx, data.ModeWrite)
	if err != nil {
		return err
	}
	if err := c.R.withLock(h.DataDir, func() error {
		if err := h.Store.Vacuum(ctx); err != nil {
			return err
		}
		// The rebuilt pages sit in the WAL until a checkpoint folds them in.
		return h.Store.Checkpoint(ctx)
	}); err != nil {
		return err
	}
	ctx.Line("database vacuumed")
	return nil
}

// Confirmed asks a y/N question and accepts only y or yes, in any case. The
// framework's Confirm is a toggle widget, so a typed answer is read instead.
func Confirmed(ctx console.Context, question string) bool {
	answer, err := ctx.Ask(question)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}
