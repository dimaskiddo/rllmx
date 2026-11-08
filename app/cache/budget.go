package cache

import (
	"sync"
	"time"
)

// Budget bounds what a group may hold in the process memory store, which has
// no size cap of its own. It tracks sizes and expiry times only, never a key or
// a value. A nil Budget admits everything, so a shared store (which has its own
// limit) needs no branch.
type Budget struct {
	MaxEntries int
	MaxBytes   int64

	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	entries []budgetEntry
	bytes   int64
}

type budgetEntry struct {
	size    int64
	expires time.Time
}

// Reserve admits an entry of size bytes that lives for ttl, or refuses it when
// either limit would be passed. Entries past their expiry are released first.
func (b *Budget) Reserve(size int, ttl time.Duration) bool {
	if b == nil {
		return true
	}
	now := time.Now
	if b.Now != nil {
		now = b.Now
	}
	t := now()
	b.mu.Lock()
	defer b.mu.Unlock()
	kept := b.entries[:0]
	for _, e := range b.entries {
		if e.expires.After(t) {
			kept = append(kept, e)
		} else {
			b.bytes -= e.size
		}
	}
	b.entries = kept
	if size < 0 || len(b.entries) >= b.MaxEntries || b.bytes+int64(size) > b.MaxBytes {
		return false
	}
	b.entries = append(b.entries, budgetEntry{size: int64(size), expires: t.Add(ttl)})
	b.bytes += int64(size)
	return true
}
