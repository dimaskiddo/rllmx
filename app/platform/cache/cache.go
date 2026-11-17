// Package cache adds to the framework's Cache facade only what it lacks: keys
// that carry a group and a generation (the facade cannot purge a group), the
// rule that credential groups stay in process memory, a read-through load that
// runs once per key, and the fail-open behavior. It defines no store.
package cache

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	gocache "github.com/goravel/framework/contracts/cache"
	"github.com/goravel/framework/contracts/log"
	"golang.org/x/sync/singleflight"
)

// Group names what a cached value is for. Only the groups listed as shared may
// use the configured shared store. Every other group, including one nobody has
// listed yet, stays in the process memory store, so a new group that holds a
// credential is safe by default.
type Group string

// The first group shares the configured store; the second never leaves the
// process, because it holds a credential or a value derived from one.
const (
	// Settings caches the secret-free projection of the settings row.
	Settings Group = "settings"
	// APIKeys caches key lookups, keyed by the hash of the key.
	APIKeys Group = "apikeys"
	// Models caches the model catalog.
	Models Group = "models"
	// Limiter caches rate-limit counters.
	Limiter Group = "limiter"
	// Response caches answers, and never crosses an API key.
	Response Group = "response"
	// ToolCalls caches tool-call state.
	ToolCalls Group = "toolcalls"

	// Connections caches connection rows, which carry credentials.
	Connections Group = "connections"
	// Refresh caches in-flight credential refreshes.
	Refresh Group = "refresh"
	// OAuth caches sign-in flow state.
	OAuth Group = "oauth"
	// Overrides caches header overrides, which may be credentials.
	Overrides Group = "overrides"
	// Discovery caches an identity provider's OpenID metadata. It holds no
	// credential and stays in memory, because a sign-in must finish on the
	// instance that started it.
	Discovery Group = "discovery"
	// Quota caches an account's quota reading, which holds no credential and is
	// stale after a minute anyway.
	Quota Group = "quota"
)

func (g Group) shared() bool {
	switch g {
	case Settings, APIKeys, Models, Limiter, Response, ToolCalls:
		return true
	}
	return false
}

const (
	// MaxValueBytes and MaxKeyBytes bound what is stored.
	MaxValueBytes = 4 << 20
	MaxKeyBytes   = 4 << 10

	memoryStore = "memory"
	sharedStore = "redis"
	loadTimeout = 30 * time.Second
)

// ErrLoadPanic is returned when a loader panics.
var ErrLoadPanic = errors.New("cache: loader panicked")

// Option changes how a Cache picks its stores.
type Option func(*Cache)

// WithoutShared keeps the shared groups in memory too, for a process with no
// Redis configured.
func WithoutShared() Option { return func(c *Cache) { c.shared = memoryStore } }

// WithLog sets where the first failure of the shared store is reported, once.
// Without it a failure is silent.
func WithLog(l log.Log) Option { return func(c *Cache) { c.log = l } }

// Cache is the facade seen through groups. No method returns an error: a cache
// failure is a miss, and a failed write is a no-op, so a request never fails
// because of the cache.
type Cache struct {
	facade gocache.Cache
	shared string
	log    log.Log

	loads  singleflight.Group
	warned atomic.Bool
}

// New wraps the Cache facade. The shared groups use its "redis" store unless
// WithoutShared is given.
func New(facade gocache.Cache, opts ...Option) *Cache {
	c := &Cache{facade: facade, shared: sharedStore}
	for _, o := range opts {
		o(c)
	}
	return c
}

// store returns nil when the facade cannot build the store, as it does for a
// Redis that is not configured.
func (c *Cache) store(g Group) gocache.Driver {
	name := memoryStore
	if g.shared() {
		name = c.shared
	}
	return c.facade.Store(name)
}

func counterKey(g Group) string { return string(g) + ":gen" }

// generation is the number a purge raised. A failed read counts as zero, which
// can only serve a value from before a purge while the store that holds the
// counter also fails to answer; the value's own TTL bounds that.
func generation(st gocache.Driver, g Group) int64 { return st.GetInt64(counterKey(g)) }

func valueKey(g Group, gen int64, key string) string {
	return string(g) + ":" + strconv.FormatInt(gen, 10) + ":" + key
}

func getAt(st gocache.Driver, g Group, gen int64, key string) ([]byte, bool) {
	s, ok := st.Get(valueKey(g, gen, key)).(string)
	if !ok {
		return nil, false
	}
	return []byte(s), true
}

// setAt stores a copy of val. A non-positive ttl, an oversized value or an
// oversized key stores nothing and removes what was there.
func setAt(st gocache.Driver, g Group, gen int64, key string, val []byte, ttl time.Duration) error {
	k := valueKey(g, gen, key)
	if ttl <= 0 || len(val) > MaxValueBytes || len(key) > MaxKeyBytes {
		st.Forget(k)
		return nil
	}
	return st.Put(k, string(val), ttl)
}

// guard runs fn and turns a panic into a failure. The Redis driver is built
// with no client when the first connection fails, and the facade keeps that
// driver, so every later call on it panics instead of returning an error. A
// failed write has no error to look at either, so the first failure of the
// process is reported once, without a value or the panic text.
func (c *Cache) guard(g Group, fn func(st gocache.Driver)) {
	st := c.store(g)
	if st == nil {
		c.failed("the store is not available")
		return
	}
	defer func() {
		if recover() != nil {
			c.failed("the store failed")
		}
	}()
	fn(st)
}

func (c *Cache) failed(what string) {
	if c.log != nil && c.warned.CompareAndSwap(false, true) {
		c.log.Warningf("cache: %s; values are loaded again and writes are skipped until it answers", what)
	}
}

// Get returns a copy of the value, or false on a miss or any failure.
func (c *Cache) Get(g Group, key string) (val []byte, ok bool) {
	c.guard(g, func(st gocache.Driver) { val, ok = getAt(st, g, generation(st, g), key) })
	return val, ok
}

// Set stores a copy of val for ttl.
func (c *Cache) Set(g Group, key string, val []byte, ttl time.Duration) {
	c.guard(g, func(st gocache.Driver) {
		if setAt(st, g, generation(st, g), key, val, ttl) != nil {
			c.failed("a write failed")
		}
	})
}

// Delete removes one value.
func (c *Cache) Delete(g Group, key string) {
	c.guard(g, func(st gocache.Driver) { st.Forget(valueKey(g, generation(st, g), key)) })
}

// Purge makes every value of a group unreachable. The facade has no purge by
// group, so this raises a counter that is part of every key; the old values
// expire by their TTL. The counter lives in the group's store, so every
// process sharing that store sees the purge.
func (c *Cache) Purge(g Group) {
	c.guard(g, func(st gocache.Driver) { _, _ = st.Increment(counterKey(g)) })
}

// Load returns the cached value, or runs load and stores its result for ttl.
// The facade's Remember does not collapse concurrent loads, so this does: one
// load runs for every caller waiting on a key. It runs on a context detached
// from the callers, so one that leaves does not fail the rest. A load error is
// returned and never cached. A result loaded before a Purge is stored under
// the old generation, which nothing reads any more.
func (c *Cache) Load(ctx context.Context, g Group, key string, ttl time.Duration, load func(context.Context) ([]byte, error)) ([]byte, error) {
	var gen int64
	var hit []byte
	var found bool
	c.guard(g, func(st gocache.Driver) {
		gen = generation(st, g)
		hit, found = getAt(st, g, gen, key)
	})
	if found {
		return hit, nil
	}
	flight := string(g) + "\x00" + strconv.FormatInt(gen, 10) + "\x00" + key
	ch := c.loads.DoChan(flight, func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loadTimeout)
		defer cancel()
		v, err := safeLoad(lctx, load)
		if err != nil {
			return nil, err
		}
		c.guard(g, func(st gocache.Driver) {
			if setAt(st, g, gen, key, v, ttl) != nil {
				c.failed("a write failed")
			}
		})
		return v, nil
	})
	select {
	case r := <-ch:
		if r.Err != nil {
			return nil, r.Err
		}
		return slices.Clone(r.Val.([]byte)), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func safeLoad(ctx context.Context, load func(context.Context) ([]byte, error)) (v []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = nil, fmt.Errorf("%w: %v", ErrLoadPanic, r)
		}
	}()
	return load(ctx)
}
