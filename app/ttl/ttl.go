// Package ttl holds a value that is read on every request and reloaded now and
// then. Readers take no lock; one caller reloads while the others wait for that
// same load, and a failed load is returned to them without being kept.
package ttl

import (
	"context"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type snapshot[T any] struct {
	val T
	at  time.Time
}

// Value caches the result of Load for a lifetime.
type Value[T any] struct {
	load  func(context.Context) (T, error)
	now   func() time.Time
	life  time.Duration
	cur   atomic.Pointer[snapshot[T]]
	gen   atomic.Uint64
	loads singleflight.Group
}

// New returns a value that reuses a load for life. A nil now means time.Now.
func New[T any](life time.Duration, now func() time.Time, load func(context.Context) (T, error)) *Value[T] {
	if now == nil {
		now = time.Now
	}
	return &Value[T]{load: load, now: now, life: life}
}

// Get returns the cached value while it is fresh, and loads it otherwise. The
// load runs on a context that outlives the caller's, so a request that leaves
// does not fail the others waiting on it, and is bounded by timeout.
func (v *Value[T]) Get(ctx context.Context, timeout time.Duration) (T, error) {
	if s := v.cur.Load(); s != nil && v.now().Sub(s.at) < v.life {
		return s.val, nil
	}
	gen := v.gen.Load()
	ch := v.loads.DoChan("load", func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		val, err := v.load(lctx)
		if err != nil {
			return nil, err
		}
		// A drop that happened while loading wins: what was read may be older
		// than the change that asked for the drop.
		if v.gen.Load() == gen {
			v.cur.Store(&snapshot[T]{val: val, at: v.now()})
		}
		return val, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			var zero T
			return zero, r.Err
		}
		return r.Val.(T), nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// Drop forgets the cached value, so the next Get loads again.
func (v *Value[T]) Drop() {
	v.gen.Add(1)
	v.cur.Store(nil)
}
