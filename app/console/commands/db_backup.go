package commands

import (
	"errors"
	"fmt"
	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"
	"os"
	"path/filepath"
	"rllmx/app/store/data"
	"rllmx/app/store/repository"
)

// DBBackup writes a full copy of the main database.
type DBBackup struct{ R *DatabaseRuntime }

func (c *DBBackup) Signature() string { return "db:backup" }

func (c *DBBackup) Description() string { return "Write a full copy of the main database" }

func (c *DBBackup) Extend() command.Extend {
	return command.Extend{Category: "db", Flags: []command.Flag{
		&command.StringFlag{Name: "out", Usage: "existing directory for the backup (default: the data directory's backups folder)"},
	}}
}

func (c *DBBackup) Handle(ctx console.Context) error {
	h, err := c.R.Open(ctx, data.ModeBackup)
	if err != nil {
		return err
	}
	dir := ctx.Option("out")
	if dir == "" {
		dir = data.BackupDir(h.DataDir)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("db: create %s: %w", filepath.Base(dir), err)
		}
	}
	file, err := h.Store.Backup(ctx, dir, "manual")
	if errors.Is(err, repository.ErrBackupUnsupported) {
		ctx.Error(err.Error())
		c.R.exit(2)
		return err
	}
	if err != nil {
		return err
	}
	ctx.Line("backup written: " + file)
	return nil
}
