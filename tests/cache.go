package tests

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/cache"
)

// ErrCacheDown is what a CacheStore returns while it is switched off.
var ErrCacheDown = errors.New("cache store is down")

// CacheStore is a cache.Driver kept in a map, with a switch that makes every
// call fail, the way a stopped Redis does. Methods the product code does not
// call panic through the nil embedded interface.
type CacheStore struct {
	cache.Driver
	mu   sync.Mutex
	data map[string]any
	ttl  map[string]time.Duration
	down bool
	// panics makes every call panic, as the Redis driver does when it was built
	// with no client because the first connection failed.
	panics bool
	// Calls counts the calls made, by method name.
	Calls map[string]int
}

// NewCacheStore returns an empty, working store.
func NewCacheStore() *CacheStore {
	return &CacheStore{data: map[string]any{}, ttl: map[string]time.Duration{}, Calls: map[string]int{}}
}

// SetDown switches failures on or off.
func (s *CacheStore) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// SetPanics makes every call panic, or stops that.
func (s *CacheStore) SetPanics(panics bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panics = panics
}

// Keys returns the stored keys that start with prefix.
func (s *CacheStore) Keys(prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// TTL returns the lifetime a key was stored with.
func (s *CacheStore) TTL(key string) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ttl[key]
}

func (s *CacheStore) call(name string) {
	s.Calls[name]++
	if s.panics {
		panic("runtime error: invalid memory address or nil pointer dereference")
	}
}

func (s *CacheStore) Put(key string, value any, t time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("Put")
	if s.down {
		return ErrCacheDown
	}
	s.data[key], s.ttl[key] = value, t
	return nil
}

func (s *CacheStore) Get(key string, def ...any) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("Get")
	if v, ok := s.data[key]; ok && !s.down {
		return v
	}
	if len(def) > 0 {
		return def[0]
	}
	return nil
}

func (s *CacheStore) Has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("Has")
	_, ok := s.data[key]
	return ok && !s.down
}

func (s *CacheStore) Forget(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("Forget")
	if s.down {
		return false
	}
	delete(s.data, key)
	return true
}

func (s *CacheStore) Increment(key string, value ...int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("Increment")
	if s.down {
		return 0, ErrCacheDown
	}
	by := int64(1)
	if len(value) > 0 {
		by = value[0]
	}
	cur, _ := s.data[key].(int64)
	s.data[key] = cur + by
	return cur + by, nil
}

func (s *CacheStore) GetInt64(key string, def ...int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.call("GetInt64")
	if v, ok := s.data[key].(int64); ok && !s.down {
		return v
	}
	if len(def) > 0 {
		return def[0]
	}
	return 0
}

// Cache is a cache.Cache over named stores.
type Cache struct {
	cache.Driver
	def    *CacheStore
	stores map[string]*CacheStore
}

// NewCache returns a cache whose default store and the stores named "memory"
// and "redis" are separate CacheStores.
func NewCache() *Cache {
	def := NewCacheStore()
	return &Cache{def: def, stores: map[string]*CacheStore{"memory": def, "redis": NewCacheStore()}}
}

// StoreNamed returns a store for assertions.
func (c *Cache) StoreNamed(name string) *CacheStore { return c.stores[name] }

// Store returns the named driver, or nil for a name that was never set up,
// which is what the facade does when a store cannot be built.
func (c *Cache) Store(name string) cache.Driver {
	if s, ok := c.stores[name]; ok {
		return s
	}
	return nil
}
