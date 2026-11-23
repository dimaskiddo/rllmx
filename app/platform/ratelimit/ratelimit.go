// Package ratelimit paces outbound calls with one token bucket per key, so a
// burst never reaches a provider faster than the configured rate. A caller
// waits; a wait longer than the configured ceiling is refused instead.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// ErrWait is returned when the queue would make a caller wait past MaxWait.
var ErrWait = errors.New("ratelimit: the wait would be too long")

// maxKeys bounds the buckets one group keeps; a database with more connections
// than this reuses them rather than growing without limit.
const maxKeys = 4096

// Config is the pace one group applies.
type Config struct {
	// RPM is the sustained requests per minute for each key; zero turns pacing
	// off.
	RPM int
	// Burst is how many calls may go out at once before the rate applies.
	Burst int
	// MaxWait bounds how long one call may queue; zero means no bound.
	MaxWait time.Duration
}

// Valid reports why a configuration cannot be used. A zero RPM is valid and
// means off.
func (c Config) Valid() error {
	switch {
	case c.RPM < 0:
		return errors.New("the rate limit must not be negative")
	case c.RPM > 0 && c.Burst < 1:
		return errors.New("the rate limit burst must be at least 1")
	case c.MaxWait < 0:
		return errors.New("the rate limit wait must not be negative")
	}
	return nil
}

// Bucket is one key's token bucket. It is safe for concurrent use.
type Bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

// NewBucket starts a full bucket that refills at perMinute/60 per second.
func NewBucket(perMinute, burst int, now func() time.Time) *Bucket {
	if now == nil {
		now = time.Now
	}
	b := &Bucket{rate: float64(perMinute) / 60, burst: float64(burst), tokens: float64(burst), now: now}
	b.last = now()
	return b
}

// take removes one token, or reports how long until one is free.
func (b *Bucket) take() (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if elapsed := b.now().Sub(b.last); elapsed > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsed.Seconds()*b.rate)
		b.last = b.last.Add(elapsed)
	}
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) / b.rate * float64(time.Second)), false
}

// Group paces calls by key, such as a connection id.
type Group struct {
	cfg   Config
	mu    sync.Mutex
	byKey map[string]*Bucket
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// NewGroup paces at cfg; a zero rate makes Wait return at once.
func NewGroup(cfg Config) *Group { return newGroup(cfg, time.Now, nap) }

func newGroup(cfg Config, now func() time.Time, sleep func(context.Context, time.Duration) error) *Group {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = nap
	}
	return &Group{cfg: cfg, byKey: map[string]*Bucket{}, now: now, sleep: sleep}
}

// Wait blocks until key may send one call. It returns ctx.Err when the call
// ends, and ErrWait when the queue would hold it longer than MaxWait. No lock
// is held while waiting.
func (g *Group) Wait(ctx context.Context, key string) error {
	if g == nil || g.cfg.RPM <= 0 {
		return nil
	}
	b := g.bucket(key)
	var waited time.Duration
	for {
		delay, ok := b.take()
		if ok {
			return nil
		}
		if g.cfg.MaxWait > 0 && waited+delay > g.cfg.MaxWait {
			return fmt.Errorf("%w: %s", ErrWait, key)
		}
		if err := g.sleep(ctx, delay); err != nil {
			return err
		}
		waited += delay
	}
}

// bucket returns the key's bucket, making one when the map has room. At the cap
// one entry is dropped rather than letting the map grow.
func (g *Group) bucket(key string) *Bucket {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b := g.byKey[key]; b != nil {
		return b
	}
	if len(g.byKey) >= maxKeys {
		for k := range g.byKey {
			delete(g.byKey, k)
			break
		}
	}
	b := NewBucket(g.cfg.RPM, g.cfg.Burst, g.now)
	g.byKey[key] = b
	return b
}

// nap sleeps for d, or returns at once when ctx ends first.
func nap(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
