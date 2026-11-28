package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeClock records what the group would have slept, without sleeping.
type fakeClock struct {
	at time.Time
}

func (c *fakeClock) now() time.Time { return c.at }

func (c *fakeClock) sleep(_ context.Context, d time.Duration) error {
	c.at = c.at.Add(d)
	return nil
}

func newTestGroup(cfg Config, c *fakeClock) *Group {
	return newGroup(cfg, c.now, c.sleep)
}

func TestABucketHoldsTheBurstThenRefillsAtTheRate(t *testing.T) {
	c := &fakeClock{at: time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)}
	g := newTestGroup(Config{RPM: 60, Burst: 2}, c)
	ctx := context.Background()
	for i := range 2 {
		if err := g.Wait(ctx, "c1"); err != nil {
			t.Fatalf("burst call %d: %v", i, err)
		}
	}
	if err := g.Wait(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if got := c.at.Sub(time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)); got != time.Second {
		t.Fatalf("the third call waited %s, want 1s at 60 rpm", got)
	}
}

func TestEachKeyHasItsOwnBucket(t *testing.T) {
	c := &fakeClock{at: time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)}
	g := newTestGroup(Config{RPM: 60, Burst: 1}, c)
	ctx := context.Background()
	if err := g.Wait(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := g.Wait(ctx, "b"); err != nil {
		t.Fatalf("a second key waited behind the first: %v", err)
	}
	if c.at != time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC) {
		t.Fatalf("a second key slept until %s", c.at)
	}
}

func TestAWaitPastTheCeilingIsRefused(t *testing.T) {
	c := &fakeClock{at: time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)}
	g := newTestGroup(Config{RPM: 6, Burst: 1, MaxWait: 3 * time.Second}, c)
	ctx := context.Background()
	if err := g.Wait(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	err := g.Wait(ctx, "a")
	if !errors.Is(err, ErrWait) {
		t.Fatalf("err = %v, want ErrWait: one token is 10s away and the ceiling is 3s", err)
	}
}

func TestACancelledCallStopsWaiting(t *testing.T) {
	c := &fakeClock{at: time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)}
	g := newTestGroup(Config{RPM: 60, Burst: 1}, c)
	g.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	ctx := context.Background()
	if err := g.Wait(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if err := g.Wait(ctx, "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context error", err)
	}
}

func TestAZeroRateDoesNothing(t *testing.T) {
	g := NewGroup(Config{RPM: 0, Burst: 0})
	if err := g.Wait(context.Background(), "a"); err != nil {
		t.Fatalf("pacing is off but Wait slept or failed: %v", err)
	}
	var none *Group
	if err := none.Wait(context.Background(), "a"); err != nil {
		t.Fatalf("a nil group failed: %v", err)
	}
}

func TestTheBucketMapStaysBounded(t *testing.T) {
	c := &fakeClock{at: time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)}
	g := newTestGroup(Config{RPM: 600, Burst: 1}, c)
	for i := range maxKeys + 50 {
		if err := g.Wait(context.Background(), string(rune(i))+"-key"); err != nil {
			t.Fatal(err)
		}
	}
	if len(g.byKey) > maxKeys {
		t.Fatalf("%d buckets, want at most %d", len(g.byKey), maxKeys)
	}
}
