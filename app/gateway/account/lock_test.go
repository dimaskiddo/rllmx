package account

import (
	"context"
	"errors"
	"testing"
	"time"

	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// blockingConns holds List until released, so a pick stays inside the lock.
type blockingConns struct {
	ConnReader
	entered chan struct{}
	release chan struct{}
}

func (b *blockingConns) List(ctx context.Context, f repository.ConnFilter) ([]repository.Connection, error) {
	b.entered <- struct{}{}
	<-b.release
	return b.ConnReader.List(ctx, f)
}

func lockRig(t *testing.T, settings string) (*rig, *blockingConns) {
	t.Helper()
	r := newRig(t, settings)
	r.add(t, "p", "a", nil)
	r.add(t, "p", "b", nil)
	b := &blockingConns{ConnReader: r.st.Connections(), entered: make(chan struct{}, 8), release: make(chan struct{})}
	src := settingsFn(func(context.Context) (document.Doc, error) { return document.From(r.conf), nil })
	r.sel = NewSelector(SelectorOptions{Conns: b, Settings: src, Log: r.log, Now: r.clock})
	return r, b
}

func TestARoundRobinPickLeavesTheQueueWhenItsRequestIsCancelled(t *testing.T) {
	r, b := lockRig(t, `{"fallbackStrategy":"round-robin"}`)
	first := make(chan error, 1)
	go func() {
		_, err := r.sel.Select(context.Background(), Request{Provider: "p"})
		first <- err
	}()
	<-b.entered

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.sel.Select(ctx, Request{Provider: "p"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want a deadline", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("the cancelled pick stayed in the queue")
	}
	close(b.release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestFillFirstPicksDoNotWaitForEachOther(t *testing.T) {
	r, b := lockRig(t, `{}`)
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := r.sel.Select(context.Background(), Request{Provider: "p"})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-b.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("a fill-first pick waited for another to finish its read")
		}
	}
	close(b.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRoundRobinPicksOfOneProviderStillRunOneAtATime(t *testing.T) {
	r, b := lockRig(t, `{"fallbackStrategy":"round-robin"}`)
	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := r.sel.Select(context.Background(), Request{Provider: "p"})
			done <- err
		}()
	}
	<-b.entered
	select {
	case <-b.entered:
		t.Fatal("two round-robin picks of one provider ran together")
	case <-time.After(60 * time.Millisecond):
	}
	close(b.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnotherProvidersRoundRobinPickIsNotQueued(t *testing.T) {
	r, b := lockRig(t, `{"fallbackStrategy":"round-robin"}`)
	r.add(t, "q", "c", nil)
	done := make(chan error, 2)
	go func() { _, err := r.sel.Select(context.Background(), Request{Provider: "p"}); done <- err }()
	<-b.entered
	go func() { _, err := r.sel.Select(context.Background(), Request{Provider: "q"}); done <- err }()
	select {
	case <-b.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("a pick for another provider waited")
	}
	close(b.release)
	for range 2 {
		<-done
	}
}
