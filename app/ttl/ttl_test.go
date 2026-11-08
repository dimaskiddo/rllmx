package ttl

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestAFreshValueIsReusedAndAStaleOneReloads(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	var loads int
	v := New(5*time.Second, c.now, func(context.Context) (int, error) { loads++; return loads, nil })

	for range 3 {
		if got, err := v.Get(context.Background(), time.Second); err != nil || got != 1 {
			t.Fatalf("got %d %v", got, err)
		}
	}
	c.add(5 * time.Second)
	if got, _ := v.Get(context.Background(), time.Second); got != 2 {
		t.Fatalf("a stale value was kept: %d", got)
	}
	if loads != 2 {
		t.Fatalf("%d loads", loads)
	}
}

func TestAFailedLoadIsReturnedAndNotKept(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	boom := errors.New("boom")
	var calls int
	v := New(time.Minute, c.now, func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", boom
		}
		return "ok", nil
	})
	if _, err := v.Get(context.Background(), time.Second); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if got, err := v.Get(context.Background(), time.Second); err != nil || got != "ok" {
		t.Fatalf("the failure was kept: %q %v", got, err)
	}
}

func TestDropMakesTheNextGetLoad(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	var loads int
	v := New(time.Hour, c.now, func(context.Context) (int, error) { loads++; return loads, nil })
	v.Get(context.Background(), time.Second)
	v.Drop()
	if got, _ := v.Get(context.Background(), time.Second); got != 2 {
		t.Fatalf("got %d after a drop", got)
	}
}

func TestADropDuringALoadIsNotUndoneByIt(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	started, release := make(chan struct{}), make(chan struct{})
	var loads atomic.Int32
	v := New(time.Hour, c.now, func(context.Context) (int, error) {
		n := int(loads.Add(1))
		if n == 1 {
			close(started)
			<-release
		}
		return n, nil
	})
	done := make(chan int)
	go func() { got, _ := v.Get(context.Background(), time.Second); done <- got }()
	<-started
	v.Drop()
	close(release)
	<-done
	if got, _ := v.Get(context.Background(), time.Second); got != 2 {
		t.Fatalf("a load that began before the drop was kept: %d", got)
	}
}

func TestManyCallersShareOneLoad(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	gate := make(chan struct{})
	var loads atomic.Int32
	v := New(time.Hour, c.now, func(context.Context) (int, error) {
		loads.Add(1)
		<-gate
		return 7, nil
	})
	var wg sync.WaitGroup
	for range 30 {
		wg.Go(func() {
			if got, err := v.Get(context.Background(), time.Second); err != nil || got != 7 {
				t.Errorf("got %d %v", got, err)
			}
		})
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()
	if n := loads.Load(); n != 1 {
		t.Fatalf("%d loads for 30 callers", n)
	}
}

func TestACallerThatLeavesDoesNotFailTheLoadOthersWaitOn(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	gate, inside := make(chan struct{}), make(chan struct{})
	v := New(time.Hour, c.now, func(ctx context.Context) (int, error) {
		close(inside)
		<-gate
		return 9, ctx.Err()
	})
	// The caller that leaves is the one whose context started the load.
	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() { _, err := v.Get(ctx, time.Second); left <- err }()
	<-inside
	stay := make(chan int, 1)
	go func() { got, _ := v.Get(context.Background(), time.Second); stay <- got }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that left got %v", err)
	}
	close(gate)
	if got := <-stay; got != 9 {
		t.Fatalf("the caller that stayed got %d", got)
	}
}

func TestALoadStopsAtItsTimeout(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	v := New(time.Hour, c.now, func(ctx context.Context) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	if _, err := v.Get(context.Background(), 30*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
}
