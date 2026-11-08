package cache

import (
	"container/heap"
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
	entries expiryHeap
	bytes   int64
}

type budgetEntry struct {
	size    int64
	expires time.Time
}

// expiryHeap keeps the entry that expires first at the top, so releasing what
// has expired costs only the entries released.
type expiryHeap []budgetEntry

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].expires.Before(h[j].expires) }
func (h expiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *expiryHeap) Push(x any)        { *h = append(*h, x.(budgetEntry)) }
func (h *expiryHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	*h = old[:n-1]
	return e
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
	for len(b.entries) > 0 && !b.entries[0].expires.After(t) {
		b.bytes -= heap.Pop(&b.entries).(budgetEntry).size
	}
	if size < 0 || len(b.entries) >= b.MaxEntries || b.bytes+int64(size) > b.MaxBytes {
		return false
	}
	heap.Push(&b.entries, budgetEntry{size: int64(size), expires: t.Add(ttl)})
	b.bytes += int64(size)
	return true
}

// Reset forgets every reservation, for the moment the group it counts was
// purged: those entries are gone, and counting them until their expiry would
// refuse new ones for no reason.
func (b *Budget) Reset() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.entries = nil
	b.bytes = 0
	b.mu.Unlock()
}
