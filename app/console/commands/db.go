package commands

import (
	"context"
	"errors"
	"os"
	"rllmx/app/store/dialect"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/store/analytics"
	"rllmx/app/store/data"
	"rllmx/app/store/lock"
	"rllmx/app/store/repository"
)

// Handle is an opened database for one command. Close releases its pool.
type Handle struct {
	Store   *repository.Store
	DataDir string
}

// AnalyticsHandle is the usage store plus what an import needs from the
// configuration database: the read-only store that opens a sealed client key,
// and the path of the main file for a default source.
type AnalyticsHandle struct {
	Store   *analytics.Store
	Keys    *repository.Store
	DataDir string
	Source  string
	Log     log.Log
	Opener  repository.Opener
}

// OpenSource opens the legacy source database for reading. The caller closes it.
func (h *AnalyticsHandle) OpenSource(path string) (*repository.Conn, error) {
	if h.Opener == nil {
		return nil, errors.New("db: no way to open another SQLite file is configured")
	}
	return h.Opener(path, dialect.BusyWait)
}

// DatabaseRuntime is everything a db command takes from the process, so a test
// replaces it. Open runs only after the command has decided to go ahead, and
// refuses a bad configuration without repeating a value.
type DatabaseRuntime struct {
	Open func(ctx context.Context, m data.Mode) (*Handle, error)
	// Analytics opens the usage store the way the server does.
	Analytics func(ctx context.Context) (*AnalyticsHandle, error)
	// Terminal reports whether standard input is a terminal.
	Terminal func() bool
	// Exit ends the process with a code. Artisan only ever exits 0 or 1, so a
	// command that needs another code calls this itself.
	Exit func(code int)
	// Facades returns the framework pieces this runtime was built over, for a
	// command that opens something other than the main store.
	Facades func() Facades
}

func (r *DatabaseRuntime) exit(code int) {
	if r.Exit != nil {
		r.Exit(code)
		return
	}
	os.Exit(code)
}

// confirm is the gate every destructive command shares: `--yes` skips it, a
// missing terminal refuses rather than reading EOF as a no, and anything but y
// or yes cancels. noun names the command, so the refusal says which one it was.
func (r *DatabaseRuntime) confirm(ctx console.Context, noun, question string) error {
	if ctx.OptionBool("yes") {
		return nil
	}
	if !r.Terminal() {
		return errors.New(noun + " needs a terminal to confirm; pass --yes to run it without one")
	}
	if !Confirmed(ctx, question) {
		return errors.New("cancelled")
	}
	return nil
}

// withLock holds the data lock for the rest of the command. The server holds the
// same file, so this is what stops a write while it is serving.
func (r *DatabaseRuntime) withLock(dir string, fn func() error) error {
	held, err := lock.Acquire(dir)
	if err != nil {
		return err
	}
	defer func() { _ = held.Release() }()
	return fn()
}
