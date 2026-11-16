package combo

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrNoChoice means the router could not name a member: the declared order
// stands.
var ErrNoChoice = errors.New("combo: the router named no member")

// Router picks the member a smart combo should try first. It is a plain choice
// question the router asks of one model; a failure is ErrNoChoice, never an
// error the client sees.
type Router interface {
	Choose(ctx context.Context, state string, models []string) (Choice, error)
}

// Choice is what the router answered.
type Choice struct {
	// Model is the member it named; it must be one of the models asked about.
	Model string
	// Confidence is 0 to 1; below the policy's threshold the order stands.
	Confidence float64
}

// stickyLife is how long a session's choice is remembered, and the most
// sessions kept at once.
const (
	stickyLife  = 2 * time.Hour
	stickyLimit = 5000
)

type stickyEntry struct {
	model string
	at    time.Time
}

// Sticky remembers which member a session chose last time. It is process-local,
// A second instance keeps its own.
type Sticky struct {
	mu    sync.Mutex
	items map[string]stickyEntry
}

// NewSticky builds an empty store.
func NewSticky() *Sticky { return &Sticky{items: map[string]stickyEntry{}} }

// Get returns the model this session chose, "" when none is remembered or it has
// aged out.
func (s *Sticky) Get(combo, session string, now time.Time) string {
	if s == nil || session == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.items[combo+":"+session]
	if !ok || now.Sub(e.at) > stickyLife {
		return ""
	}
	return e.model
}

// Put records the model a session chose, keeping the store bounded: when it is
// full the oldest entry goes, so one scan, not one per write.
func (s *Sticky) Put(combo, session, model string, now time.Time) {
	if s == nil || session == "" || model == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := combo + ":" + session
	s.items[key] = stickyEntry{model: model, at: now}
	if len(s.items) <= stickyLimit {
		return
	}
	oldestKey := ""
	var oldestAt time.Time
	for k, e := range s.items {
		if k == key {
			continue
		}
		if oldestKey == "" || e.at.Before(oldestAt) {
			oldestKey, oldestAt = k, e.at
		}
	}
	if oldestKey != "" {
		delete(s.items, oldestKey)
	}
}

// len reports how many sessions are remembered; a test asserts the bound.
func (s *Sticky) len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// Drop forgets one combo's entries, for a combo whose members changed.
func (s *Sticky) Drop(combo string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.items {
		if len(k) > len(combo) && k[:len(combo)+1] == combo+":" {
			delete(s.items, k)
		}
	}
}
