package data

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"rllmx/app/store/lock"
	"rllmx/tests"
	"rllmx/tests/store"
)

type fakeOpen struct {
	dir    string
	st     *store.Store
	err    error
	opened atomic.Int32
	closed atomic.Int32
}

func (f *fakeOpen) open(ctx context.Context) (*Opened, error) {
	f.opened.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &Opened{
		Store:    f.st.Store,
		Warnings: nil,
		Close:    func() error { f.closed.Add(1); return nil },
	}, nil
}

func newRunner(t *testing.T) (*Runner, *fakeOpen, *tests.Log) {
	t.Helper()
	l := &tests.Log{}
	dir := t.TempDir()
	f := &fakeOpen{dir: dir, st: store.New(t)}
	r := NewRunner(Options{
		DataDir: dir, Open: f.open, Gate: NewGate(), Log: l, Context: context.Background(),
	})
	return r, f, l
}

func runAsync(r *Runner) <-chan error {
	done := make(chan error, 1)
	go func() { done <- r.Run() }()
	return done
}

func TestTheRunnerIdentifiesItselfAndStopsAfterTheServer(t *testing.T) {
	r, _, _ := newRunner(t)
	if r.Signature() != "rllmx:data" || !r.ShouldRun() {
		t.Fatalf("signature %q ShouldRun %v", r.Signature(), r.ShouldRun())
	}
	if r.ShutdownPriority() <= 0 {
		t.Fatalf("priority %d: the stores must close after the HTTP server (-100) and the schedule (0)", r.ShutdownPriority())
	}
}

func TestRunTakesTheLockOpensTheStoreAndOpensTheGate(t *testing.T) {
	r, f, _ := newRunner(t)
	done := runAsync(r)
	if !r.opts.Gate.Wait(context.Background()) {
		t.Fatal("the gate did not open")
	}
	if f.opened.Load() != 1 {
		t.Fatalf("the store was opened %d times", f.opened.Load())
	}
	if _, err := lock.Acquire(f.dir); !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("the data directory lock must be held while the runner runs: %v", err)
	}
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a normal stop is not an error: %v", err)
	}
	if f.closed.Load() != 1 {
		t.Fatalf("the store was closed %d times", f.closed.Load())
	}
	l, err := lock.Acquire(f.dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	_ = l.Release()
}

func TestAHeldLockFailsTheRunBeforeAnyStoreIsOpened(t *testing.T) {
	r, f, _ := newRunner(t)
	held, err := lock.Acquire(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	err = r.Run()
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("err = %v", err)
	}
	if f.opened.Load() != 0 {
		t.Fatal("a store was opened without the lock")
	}
	if r.opts.Gate.Wait(context.Background()) {
		t.Fatal("the gate opened although the start failed")
	}
}

func TestAFailedOpenReleasesTheLockAndFailsTheGate(t *testing.T) {
	r, f, _ := newRunner(t)
	f.err = errors.New("repository: DB_ENCRYPTION_SECRET does not match this database")
	err := r.Run()
	if err == nil || !strings.Contains(err.Error(), "DB_ENCRYPTION_SECRET") {
		t.Fatalf("err = %v", err)
	}
	if r.opts.Gate.Wait(context.Background()) {
		t.Fatal("the gate opened although the store did not")
	}
	l, lerr := lock.Acquire(f.dir)
	if lerr != nil {
		t.Fatalf("a failed start left the lock held: %v", lerr)
	}
	_ = l.Release()
}

func TestWarningsFromTheOpenAreLoggedAndNeverFailTheStart(t *testing.T) {
	r, f, l := newRunner(t)
	r.opts.Open = func(ctx context.Context) (*Opened, error) {
		o, err := f.open(ctx)
		o.Warnings = []string{"2 rows hold damaged JSON; their secrets were left as they are"}
		return o, err
	}
	done := runAsync(r)
	if !r.opts.Gate.Wait(context.Background()) {
		t.Fatal("a warning stopped the start")
	}
	_ = r.Shutdown()
	<-done
	found := false
	for _, line := range l.Lines() {
		found = found || strings.Contains(line, "damaged JSON")
	}
	if !found {
		t.Fatalf("the warning was not logged: %v", l.Lines())
	}
}

func TestShutdownBeforeRunIsHarmlessAndRunThenDoesNothing(t *testing.T) {
	r, f, _ := newRunner(t)
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(); err != nil {
		t.Fatalf("Run after Shutdown: %v", err)
	}
	if f.opened.Load() != 0 {
		t.Fatal("the store was opened after the runner was shut down")
	}
	if r.opts.Gate.Wait(context.Background()) {
		t.Fatal("the gate opened after a shutdown")
	}
}

func TestShutdownIsIdempotent(t *testing.T) {
	r, f, _ := newRunner(t)
	done := runAsync(r)
	r.opts.Gate.Wait(context.Background())
	for range 3 {
		if err := r.Shutdown(); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	if f.closed.Load() != 1 {
		t.Fatalf("the store was closed %d times", f.closed.Load())
	}
}

func TestACancelledStartDoesNotOpenTheStore(t *testing.T) {
	r, f, _ := newRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.opts.Context = ctx
	err := r.Run()
	if err == nil {
		t.Fatal("a cancelled start succeeded")
	}
	if f.opened.Load() != 0 {
		t.Fatal("the store was opened for a cancelled start")
	}
	l, lerr := lock.Acquire(f.dir)
	if lerr != nil {
		t.Fatalf("the lock stayed held: %v", lerr)
	}
	_ = l.Release()
}

func TestTheLockFileLivesInTheDataDirectory(t *testing.T) {
	r, f, _ := newRunner(t)
	done := runAsync(r)
	r.opts.Gate.Wait(context.Background())
	if _, err := os.Stat(filepath.Join(f.dir, lock.FileName)); err != nil {
		t.Fatalf("no lock file in the data directory: %v", err)
	}
	_ = r.Shutdown()
	<-done
}

func TestTheStoreIsAvailableToTheJobsOnceTheGateIsOpen(t *testing.T) {
	r, f, _ := newRunner(t)
	if r.Store() != nil || r.Analytics() != nil || r.Writer() != nil {
		t.Fatal("a store, a usage store or a writer before the start")
	}
	done := runAsync(r)
	r.opts.Gate.Wait(context.Background())
	if r.Store() != f.st.Store {
		t.Fatal("the open store is not available")
	}
	if r.Analytics() != nil || r.Writer() != nil {
		t.Fatal("an open that handed over no usage store must not expose one")
	}
	_ = r.Shutdown()
	<-done
}

func TestOnReadyRunsOnceAfterTheGateOpens(t *testing.T) {
	r, _, _ := newRunner(t)
	var calls atomic.Int32
	var gateWasOpen atomic.Bool
	r.opts.OnReady = func() {
		calls.Add(1)
		gateWasOpen.Store(r.opts.Gate.Wait(context.Background()))
	}
	done := runAsync(r)
	r.opts.Gate.Wait(context.Background())
	_ = r.Shutdown()
	<-done
	if calls.Load() != 1 || !gateWasOpen.Load() {
		t.Fatalf("OnReady ran %d times, gate open when it ran: %v", calls.Load(), gateWasOpen.Load())
	}
}

func TestOnReadyDoesNotRunWhenTheStartFails(t *testing.T) {
	r, f, _ := newRunner(t)
	f.err = errors.New("no store")
	var calls atomic.Int32
	r.opts.OnReady = func() { calls.Add(1) }
	_ = r.Run()
	if calls.Load() != 0 {
		t.Fatal("the start-up run happened without stores")
	}
}

func TestTheLockIsReleasedEvenWhenTheStoreFailsToClose(t *testing.T) {
	r, f, _ := newRunner(t)
	r.opts.Open = func(ctx context.Context) (*Opened, error) {
		o, err := f.open(ctx)
		o.Close = func() error { return errors.New("close failed") }
		return o, err
	}
	done := runAsync(r)
	r.opts.Gate.Wait(context.Background())
	if err := r.Shutdown(); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("err = %v", err)
	}
	<-done
	l, err := lock.Acquire(f.dir)
	if err != nil {
		t.Fatalf("a failing close left the lock held: %v", err)
	}
	_ = l.Release()
}
