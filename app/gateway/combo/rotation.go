package combo

import (
	"sync"

	"rllmx/app/llm/routing"
)

// Rotation is the per-combo round-robin position. It lives in this process, as
// A second instance keeps its own, so the two rotate independently.
type Rotation struct {
	mu    sync.Mutex
	state map[string]rotation
}

type rotation struct {
	index int
	count int
}

// NewRotation builds an empty rotation table.
func NewRotation() *Rotation { return &Rotation{state: map[string]rotation{}} }

// Rotate moves the list left by the combo's current index and records that this
// start has now served one more request; after limit requests it advances to the
// next member. A limit below one counts as one. Rotation happens whether the
// request succeeds or not.
func (r *Rotation) Rotate(name string, members []routing.Member, limit int) []routing.Member {
	if limit < 1 {
		limit = 1
	}
	if len(members) == 0 {
		return members
	}
	r.mu.Lock()
	s := r.state[name]
	out := make([]routing.Member, 0, len(members))
	start := s.index % len(members)
	out = append(out, members[start:]...)
	out = append(out, members[:start]...)
	s.count++
	if s.count >= limit {
		s.index = (s.index + 1) % len(members)
		s.count = 0
	}
	r.state[name] = s
	r.mu.Unlock()
	return out
}

// Reset forgets one combo's rotation, for a combo whose members just changed.
func (r *Rotation) Reset(name string) {
	r.mu.Lock()
	delete(r.state, name)
	r.mu.Unlock()
}
