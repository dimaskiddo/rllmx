package providers

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rllmx/app/gateway/probe"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

func TestTheQueueCollapsesDuplicates(t *testing.T) {
	q := newProbeQueue()
	q.enqueue("c1")
	q.enqueue("c1")
	q.enqueue("c2")
	if got := q.len(); got != 2 {
		t.Fatalf("%d pending, want 2", got)
	}
	id, ok := q.next(context.Background())
	if !ok || id != "c1" {
		t.Fatalf("%q %v", id, ok)
	}
	if got := q.len(); got != 1 {
		t.Fatalf("%d pending after one take", got)
	}
	// A connection that was probed can be queued again.
	q.enqueue("c1")
	if got := q.len(); got != 2 {
		t.Fatalf("%d pending after re-queueing", got)
	}
}

func TestAFullQueueDropsWithoutBlocking(t *testing.T) {
	q := newProbeQueue()
	for i := range queueCap {
		q.enqueue(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if got := q.len(); got != queueCap {
		t.Fatalf("%d pending, want %d", got, queueCap)
	}
	done := make(chan struct{})
	go func() {
		q.enqueue("overflow")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("enqueue blocked on a full queue")
	}
	if got := q.len(); got != queueCap {
		t.Fatalf("%d pending after a dropped job", got)
	}
}

func TestNextStopsAtShutdown(t *testing.T) {
	q := newProbeQueue()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := q.next(ctx); ok {
		t.Fatal("a cancelled runner took a job")
	}
}

// emptyProber is a connection with no model to ping.
type emptyProber struct{}

func (emptyProber) Connection(context.Context, repository.Connection, bool) []probe.Result {
	return nil
}

// pingingProber answers one model.
type pingingProber struct{}

func (pingingProber) Connection(context.Context, repository.Connection, bool) []probe.Result {
	return []probe.Result{{OK: true}}
}

func TestAConnectionWithNoModelGetsItsKeyChecked(t *testing.T) {
	for name, tc := range map[string]struct {
		prober connProber
		check  bool
	}{
		"no model":  {emptyProber{}, true},
		"one model": {pingingProber{}, false},
	} {
		checked := 0
		probeThenCheck(context.Background(), tc.prober, repository.Connection{ID: "c1"}, func(context.Context) error {
			checked++
			return nil
		}, nil)
		if got := checked > 0; got != tc.check {
			t.Errorf("%s: checked %v, want %v", name, got, tc.check)
		}
	}
}

func TestTheRunnerEndsWhenShutdownCancelsIt(t *testing.T) {
	q := newProbeQueue()
	src := &ProbeSource{q: q}
	r := &probeRunner{probes: src, ctx: context.Background(), cancel: func() {}}
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	done := make(chan error, 1)
	go func() { done <- r.Run() }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("the runner did not end at shutdown")
	}
}

func TestAKeyCheckThatCouldNotRunIsLogged(t *testing.T) {
	lg := &tests.Log{}
	probeThenCheck(context.Background(), emptyProber{}, repository.Connection{ID: "c1"}, func(context.Context) error {
		return errors.New("store unavailable")
	}, lg)
	if !strings.Contains(strings.Join(lg.Lines(), "\n"), "store unavailable") {
		t.Fatalf("the failure left no trace: %q", strings.Join(lg.Lines(), "\n"))
	}
}
