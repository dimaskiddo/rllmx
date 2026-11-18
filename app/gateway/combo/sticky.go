package combo

import (
	"container/list"
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrNoChoice means the router could not name a member: the declared order
// stands.
var ErrNoChoice = errors.New("combo: the router named no member")

// Router picks the member a smart combo should try first. It is a plain choice
// question put to routerModel; a failure is ErrNoChoice, never an error the
// client sees.
type Router interface {
	Choose(ctx context.Context, routerModel, state string, models []string) (Choice, error)
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
	key   string
	model string
	at    time.Time
}

// Sticky remembers which member a session chose last time. It is process-local,
// A second instance keeps its own. The list is in order of last write, oldest
// first, so a full store drops its oldest entry without a scan.
type Sticky struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List // of *stickyEntry
}

// NewSticky builds an empty store.
func NewSticky() *Sticky {
	return &Sticky{items: map[string]*list.Element{}, order: list.New()}
}

// Get returns the model this session chose, "" when none is remembered or it has
// aged out.
func (s *Sticky) Get(combo, session string, now time.Time) string {
	if s == nil || session == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.items[combo+":"+session]
	if !ok {
		return ""
	}
	e := el.Value.(*stickyEntry)
	if now.Sub(e.at) > stickyLife {
		return ""
	}
	return e.model
}

// Put records the model a session chose, keeping the store bounded: when it is
// full the oldest write goes.
func (s *Sticky) Put(combo, session, model string, now time.Time) {
	if s == nil || session == "" || model == "" {
		return
	}
	key := combo + ":" + session
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[key]; ok {
		e := el.Value.(*stickyEntry)
		e.model, e.at = model, now
		s.order.MoveToBack(el)
		return
	}
	s.items[key] = s.order.PushBack(&stickyEntry{key: key, model: model, at: now})
	for len(s.items) > stickyLimit {
		oldest := s.order.Front()
		s.order.Remove(oldest)
		delete(s.items, oldest.Value.(*stickyEntry).key)
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
	prefix := combo + ":"
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, el := range s.items {
		if strings.HasPrefix(k, prefix) {
			s.order.Remove(el)
			delete(s.items, k)
		}
	}
}
