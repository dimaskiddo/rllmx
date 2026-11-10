package commands

import (
	"errors"
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"
	"rllmx/app/store/data"
)

// DBCheck runs the integrity check and the schema comparison.
type DBCheck struct{ R *DatabaseRuntime }

func (c *DBCheck) Signature() string { return "db:check" }

func (c *DBCheck) Description() string { return "Check database integrity and schema; changes nothing" }

func (c *DBCheck) Extend() command.Extend { return command.Extend{Category: "db"} }

func (c *DBCheck) Handle(ctx console.Context) error {
	h, err := c.R.Open(ctx, data.ModeRead)
	if err != nil {
		return err
	}
	rep, err := h.Store.Check(ctx)
	if err != nil {
		return err
	}
	if rep.OK() {
		ctx.Line("database ok")
		return nil
	}
	if rep.Integrity != "ok" {
		ctx.Line("integrity check failed: " + rep.Integrity)
	}
	for _, p := range rep.Problems {
		ctx.Line("schema: " + p)
	}
	return errors.New("database check found problems")
}
