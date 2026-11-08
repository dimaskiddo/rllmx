package saml

import (
	"sync"
	"time"
)

// The replay set's limits, from PRD 2.
const (
	maxReplay     = 100000
	maxReplayLife = 24 * time.Hour
)

// Replay remembers assertion ids that have been used, so a captured response
// cannot be presented twice. It lives in process memory: a sign-in must finish on
// the instance that started it, and a shared set would need a lock on the
// request path for no benefit.
type Replay struct {
	mu      sync.Mutex
	entries map[string]time.Time
	Now     func() time.Time
}

// NewReplay builds an empty set.
func NewReplay() *Replay {
	return &Replay{entries: map[string]time.Time{}, Now: time.Now}
}

// Take records an assertion id until until, and reports whether it was new.
// false means the id has already been used and the response is a replay.
func (r *Replay) Take(id string, until, now time.Time) bool {
	if until.After(now.Add(maxReplayLife)) {
		until = now.Add(maxReplayLife)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if wasUsed, ok := r.entries[id]; ok {
		// An entry whose assertion can no longer be valid is not a replay: the
		// window it protected has closed.
		if now.Before(wasUsed) {
			return false
		}
		delete(r.entries, id)
	}
	if len(r.entries) >= maxReplay {
		// Everything is live, which means a burst or an attacker. Refusing this
		// sign-in is safer than forgetting another id and opening a replay window
		// for it.
		r.dropExpired(now)
		if len(r.entries) >= maxReplay {
			return false
		}
	}
	r.entries[id] = until
	return true
}

// Len reports how many ids are remembered, for a test.
func (r *Replay) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// dropExpired removes ids whose assertion can no longer be valid. Called under
// the lock.
func (r *Replay) dropExpired(now time.Time) {
	for id, until := range r.entries {
		if now.After(until) {
			delete(r.entries, id)
		}
	}
}
