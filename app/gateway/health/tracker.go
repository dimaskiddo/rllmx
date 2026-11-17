package health

import (
	"math"
	"sync"
	"time"
)

// The breaker's fixed constants. None of them is a setting: no operator has
// asked to tune them, and a wrong value is a support call either way.
const (
	// emaAlpha weights the newest latency sample. Only a success carries one.
	emaAlpha = 0.2
	// failThreshold is how many failures in a row open the breaker.
	failThreshold = 5
	// openFor is how long an open breaker turns everything away; after it, one
	// outcome decides.
	openFor = 60 * time.Second
	// decayAt is when the two counters are scaled back, so a provider that
	// worked for a month and broke today does not look healthy forever.
	decayAt = 100
)

// Circuit states, as the dashboard reads them.
const (
	Closed   = "closed"
	Open     = "open"
	HalfOpen = "half-open"
)

// State is one provider's health as the dashboard shows it.
type State struct {
	EMALatency       int64    `json:"emaLatency"`
	SuccessCount     int64    `json:"successCount"`
	FailCount        int64    `json:"failCount"`
	SuccessRate      *float64 `json:"successRate"`
	CircuitState     string   `json:"circuitState"`
	ConsecutiveFails int      `json:"consecutiveFails"`
}

type entry struct {
	ema         float64
	success     float64
	fail        float64
	consecutive int
	openedAt    time.Time
}

// Tracker holds one provider's health for this process. Nothing is persisted:
// a restart starts every provider clean, which is what a memory-only signal can
// honestly claim.
type Tracker struct {
	mu    sync.Mutex
	items map[string]*entry
	now   func() time.Time
}

// NewTracker builds an empty tracker.
func NewTracker(now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	return &Tracker{items: map[string]*entry{}, now: now}
}

// Record notes one finished attempt. A failure counts only when the account
// loop moved on: a request-scoped answer is not the provider's fault.
func (t *Tracker) Record(provider string, ok bool, latency time.Duration) {
	if t == nil || provider == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, found := t.items[provider]
	if !found {
		e = &entry{}
		t.items[provider] = e
	}
	now := t.now()
	if ok {
		if ms := latency.Seconds() * 1000; ms > 0 {
			if e.ema == 0 {
				e.ema = ms
			} else {
				e.ema = e.ema*(1-emaAlpha) + ms*emaAlpha
			}
		}
		e.decay()
		e.success++
		e.consecutive = 0
		// A success closes the breaker whatever it was doing, including a
		// half-open trial. Leaving it open on a success is how the reference
		// gets a provider stuck open while it serves.
		e.openedAt = time.Time{}
		return
	}
	e.decay()
	e.fail++
	e.consecutive++
	if e.consecutive < failThreshold {
		return
	}
	// Opened, or a half-open trial that failed: both start the wait again.
	if e.openedAt.IsZero() || now.Sub(e.openedAt) >= openFor {
		e.openedAt = now
	}
}

// decay scales both counters back once they are full, so the rate keeps meaning
// something and no counter grows without bound.
func (e *entry) decay() {
	if e.success+e.fail < decayAt {
		return
	}
	e.success *= 0.99
	e.fail *= 0.99
}

// Open reports whether this provider is currently turned away. It reads; the
// half-open state is derived, never stored, so a read cannot change routing.
func (t *Tracker) Open(provider string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	e, found := t.items[provider]
	if !found || e.openedAt.IsZero() {
		return false
	}
	return t.now().Sub(e.openedAt) < openFor
}

// Snapshot copies every provider's state for the dashboard feed.
func (t *Tracker) Snapshot() map[string]State {
	if t == nil {
		return map[string]State{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]State, len(t.items))
	now := t.now()
	for name, e := range t.items {
		s := State{
			EMALatency:       int64(math.Round(e.ema)),
			SuccessCount:     int64(math.Round(e.success)),
			FailCount:        int64(math.Round(e.fail)),
			CircuitState:     Closed,
			ConsecutiveFails: e.consecutive,
		}
		if total := e.success + e.fail; total > 0 {
			rate := e.success / total
			s.SuccessRate = &rate
		}
		if !e.openedAt.IsZero() {
			s.CircuitState = Open
			if now.Sub(e.openedAt) >= openFor {
				s.CircuitState = HalfOpen
			}
		}
		out[name] = s
	}
	return out
}
