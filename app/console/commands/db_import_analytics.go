package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rllmx/app/dialect"
	"strings"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/analytics"
	"rllmx/app/data"
	"rllmx/app/repository"
)

// DBImportAnalytics moves the usage history of a legacy database into the
// analytics store.
type DBImportAnalytics struct{ R *DatabaseRuntime }

func (c *DBImportAnalytics) Signature() string { return "db:import-analytics" }

func (c *DBImportAnalytics) Description() string {
	return "Move usage history from a legacy database into the analytics store; a rerun adds nothing"
}

func (c *DBImportAnalytics) Extend() command.Extend {
	return command.Extend{Category: "db", Flags: []command.Flag{
		&command.StringFlag{Name: "from", Usage: "legacy database file (default: the main database of this data directory)"},
		&command.BoolFlag{Name: "drop-old", Usage: "after a verified import, drop usageHistory, usageDaily and requestDetails from the source"},
	}}
}

func (c *DBImportAnalytics) Handle(ctx console.Context) error {
	dropOld := ctx.OptionBool("drop-old")
	if dropOld && !c.R.Terminal() {
		return errors.New("--drop-old needs a terminal to confirm; run it in a terminal")
	}
	h, err := c.R.Analytics(ctx)
	if err != nil {
		return err
	}
	src := ctx.Option("from")
	if src == "" {
		src = h.Source
	}
	if src == "" {
		return errors.New("db: no source database; pass --from")
	}
	if info, err := os.Stat(src); err != nil || info.IsDir() {
		return fmt.Errorf("db: source database %s", filepath.Base(src))
	}
	return c.R.withLock(h.DataDir, func() error { return c.importInto(ctx, h, src, dropOld) })
}

// importInto reads the source and verifies what landed. The caller holds the
// lock and has already removed its work folder.
func (c *DBImportAnalytics) importInto(ctx console.Context, h *AnalyticsHandle, src string, dropOld bool) error {
	cleanup := takeWorkDir(h.DataDir)
	defer cleanup()
	opt := analytics.ImportOptions{WorkDir: ImportWorkDir(h.DataDir)}
	if h.Keys != nil {
		opt.OpenKey = h.Keys.OpenAPIKey
	}
	rep, err := h.Store.ImportUsage(ctx, src, opt)
	if err != nil {
		return err
	}
	ctx.Line(fmt.Sprintf("read %d rows: %d new, %d already imported, %d unreadable", rep.Rows, rep.Inserted, rep.Skipped, rep.Invalid))
	if rep.Invalid > 0 {
		ctx.Line("rows with an unreadable timestamp were skipped; the old tables are kept")
	}
	if err := rep.Verify(); err != nil {
		c.R.exit(1)
		return err
	}
	ctx.Line(fmt.Sprintf("verified: %d rows, %d prompt tokens, %d completion tokens, cost %.6f",
		rep.Imported.Rows, rep.Imported.PromptTokens, rep.Imported.CompletionTokens, rep.Imported.Cost))
	if !dropOld {
		return nil
	}
	if rep.Invalid > 0 {
		c.R.exit(1)
		return errors.New("not dropping the old tables: some rows could not be imported")
	}
	return c.dropOld(ctx, h, src)
}

// dropOld removes the three legacy usage tables after a backup. The word DROP
// is typed: the old program can still open the file, but its usage is gone.
func (c *DBImportAnalytics) dropOld(ctx console.Context, h *AnalyticsHandle, src string) error {
	ctx.Line("This drops usageHistory, usageDaily and requestDetails from " + src + ".")
	ctx.Line("The legacy program can still open the file but loses all of its usage history.")
	answer, err := ctx.Ask("Type DROP to continue")
	if err != nil || strings.TrimSpace(answer) != "DROP" {
		c.R.exit(1)
		return errors.New("cancelled")
	}
	file, err := c.backupSource(ctx, h, src)
	if err != nil {
		return fmt.Errorf("backup before dropping failed, nothing was dropped: %w", err)
	}
	ctx.Line("backup written: " + file)
	if err := h.Store.DropLegacyTables(ctx, src, "DROP"); err != nil {
		return err
	}
	ctx.Line("old usage tables dropped; run rllmx artisan db:vacuum to reclaim the space")
	return nil
}

// backupSource copies the legacy source into the data directory's backups
// folder through the same store code the automatic backups use.
func (c *DBImportAnalytics) backupSource(ctx console.Context, h *AnalyticsHandle, src string) (string, error) {
	conn, err := h.OpenSource(src)
	if err != nil {
		return "", err
	}
	defer func() { _ = conn.Close() }()
	store, err := repository.New(repository.Options{
		DB: conn.DB, Schema: conn.Schema, Log: h.Log, Dialect: dialect.SQLite,
		BackupDir: data.BackupDir(h.DataDir),
	})
	if err != nil {
		return "", err
	}
	dir := data.BackupDir(h.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return store.Backup(ctx, dir, "before-drop")
}
