package data

import (
	"context"
	"fmt"
	"sync"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/analytics"
	"rllmx/app/lock"
	"rllmx/app/repository"
)

const (
	runnerSignature = "rllmx:data"
	// shutdownPriority puts the stores after the HTTP server (-100) and the
	// schedule runner (0): nothing that reads them is still running.
	shutdownPriority = 100
)

// Opened is what a successful open hands the runner.
type Opened struct {
	Store *repository.Store
	// Analytics is the usage store, nil when the open did not get that far.
	Analytics *analytics.Store
	// Writer batches usage rows into Analytics.
	Writer *analytics.Writer
	// Warnings are logged, never fatal.
	Warnings []string
	// Close releases the stores in the order they must go.
	Close func() error
}

// Options builds a Runner.
type Options struct {
	// DataDir holds the lock file.
	DataDir string
	// Open connects, migrates and seals. It runs only once the lock is held.
	Open func(ctx context.Context) (*Opened, error)
	// Gate is opened when the stores are ready, failed when they cannot be.
	Gate *Gate
	Log  log.Log
	// Context is the application context; a start that finds it ended opens
	// nothing.
	Context context.Context
	// OnReady runs once, after the gate has opened. The start-up maintenance
	// run goes here, so serving does not wait for it.
	OnReady func()
}

// Runner is the one provider runner that owns the data: the lock, the stores
// and, later, the usage writer. The framework starts every runner at once and
// orders only the shutdown, so this runner does its steps in sequence and tells
// the HTTP server through the gate.
type Runner struct {
	opts Options

	mu      sync.Mutex
	lock    *lock.Lock
	opened  *Opened
	stopped bool
}

// NewRunner builds the runner. Nothing opens until Run is called, so a command that boots the provider takes no lock and starts no store.
func NewRunner(o Options) *Runner { return &Runner{opts: o} }

// Signature names the runner.
func (r *Runner) Signature() string { return runnerSignature }

// ShouldRun reports that the runner always runs.
func (r *Runner) ShouldRun() bool { return true }

// ShutdownPriority orders the stop: groups go in ascending order.
func (r *Runner) ShutdownPriority() int { return shutdownPriority }

// Store returns the open configuration store, or nil before the start is done.
func (r *Runner) Store() *repository.Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opened == nil {
		return nil
	}
	return r.opened.Store
}

// Analytics returns the open usage store, or nil before the start is done.
func (r *Runner) Analytics() *analytics.Store {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opened == nil {
		return nil
	}
	return r.opened.Analytics
}

// Writer returns the usage writer, or nil before the start is done.
func (r *Runner) Writer() *analytics.Writer {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.opened == nil {
		return nil
	}
	return r.opened.Writer
}

// Run takes the data directory lock, opens the stores and opens the gate. It
// returns when the start is done; the stores stay open until Shutdown. A
// failure releases whatever was taken and fails the gate.
func (r *Runner) Run() error {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		r.opts.Gate.Fail()
		return nil
	}
	r.mu.Unlock()

	if err := r.opts.Context.Err(); err != nil {
		r.opts.Gate.Fail()
		return fmt.Errorf("data: start cancelled: %w", err)
	}
	held, err := lock.Acquire(r.opts.DataDir)
	if err != nil {
		r.opts.Gate.Fail()
		return err
	}
	opened, err := r.opts.Open(r.opts.Context)
	if err != nil {
		_ = held.Release()
		r.opts.Gate.Fail()
		return err
	}
	for _, w := range opened.Warnings {
		r.opts.Log.Warning(w)
	}

	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		_ = opened.Close()
		_ = held.Release()
		r.opts.Gate.Fail()
		return nil
	}
	r.lock, r.opened = held, opened
	r.mu.Unlock()
	r.opts.Gate.Open()
	if r.opts.OnReady != nil {
		r.opts.OnReady()
	}
	return nil
}

// Shutdown closes the stores and releases the lock. It is safe to call twice,
// and before Run.
func (r *Runner) Shutdown() error {
	r.mu.Lock()
	r.stopped = true
	held, opened := r.lock, r.opened
	r.lock, r.opened = nil, nil
	r.mu.Unlock()
	r.opts.Gate.Fail()

	var err error
	if opened != nil && opened.Close != nil {
		err = opened.Close()
	}
	if rerr := held.Release(); err == nil {
		err = rerr
	}
	return err
}
