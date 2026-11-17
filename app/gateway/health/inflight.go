package health

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// errorWindow is how long a failure keeps its provider in the live feed.
const errorWindow = 10 * time.Second

// Active is one provider, model and account with requests in flight.
type Active struct {
	Provider     string
	Model        string
	ConnectionID string
	Count        int
}

type flightSlot struct {
	provider, model, connectionID string
}

// Inflight counts the requests running right now. A request holds a slot from
// the moment an account is chosen until it ends, so a long stream stays
// counted, and a release after a request ends cannot touch another one.
type Inflight struct {
	mu     sync.Mutex
	now    func() time.Time
	counts map[flightSlot]int
	last   string
	lastAt time.Time

	gen atomic.Uint64
}

// NewInflight returns an empty table. A nil now takes time.Now.
func NewInflight(now func() time.Time) *Inflight {
	if now == nil {
		now = time.Now
	}
	return &Inflight{now: now, counts: map[flightSlot]int{}}
}

// Start takes one slot. The returned release is safe to call twice and records
// a failed provider for the live feed when failed is set.
func (f *Inflight) Start(provider, model, connID string) (release func(failed bool)) {
	if f == nil {
		return func(bool) {}
	}
	key := flightSlot{provider, model, connID}
	f.mu.Lock()
	f.counts[key]++
	f.mu.Unlock()
	f.gen.Add(1)

	var once sync.Once
	return func(failed bool) {
		once.Do(func() {
			f.mu.Lock()
			if n := f.counts[key]; n <= 1 {
				delete(f.counts, key)
			} else {
				f.counts[key] = n - 1
			}
			if failed {
				f.last, f.lastAt = strings.ToLower(provider), f.now()
			}
			f.mu.Unlock()
			f.gen.Add(1)
		})
	}
}

// Active lists the slots in use, ordered by provider, model and account.
func (f *Inflight) Active() []Active {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	out := make([]Active, 0, len(f.counts))
	for k, n := range f.counts {
		out = append(out, Active{Provider: k.provider, Model: k.model, ConnectionID: k.connectionID, Count: n})
	}
	f.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.ConnectionID < b.ConnectionID
	})
	return out
}

// ErrorProvider is the provider of the latest failure, lower-cased, while it is
// fresh; empty otherwise.
func (f *Inflight) ErrorProvider() string {
	if f == nil {
		return ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.last == "" || f.now().Sub(f.lastAt) > errorWindow {
		return ""
	}
	return f.last
}

// Generation moves on every change, so a feed can tell whether it must resend.
func (f *Inflight) Generation() uint64 {
	if f == nil {
		return 0
	}
	return f.gen.Load()
}
