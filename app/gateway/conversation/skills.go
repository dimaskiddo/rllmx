package conversation

import (
	"container/list"
	"sync"
	"time"
)

// Caps on the per-session skill table: the reference's size, lifetime and
// per-session skill list.
const (
	maxSessions      = 2000
	sessionLife      = 4 * time.Hour
	maxSessionSkills = 64
)

type sessionState struct {
	key       string
	skills    map[string]bool
	failovers int
	lastUsed  time.Time
}

// SkillSessions remembers, per conversation, which skills were already injected
// and whether an account failover happened. It holds ids and a count only, never
// prompt or message text. Safe for concurrent use.
type SkillSessions struct {
	now func() time.Time

	mu    sync.Mutex
	byKey map[string]*list.Element
	order *list.List // of *sessionState, oldest use first
}

// NewSkillSessions returns an empty table. now is the clock; nil means time.Now.
func NewSkillSessions(now func() time.Time) *SkillSessions {
	if now == nil {
		now = time.Now
	}
	return &SkillSessions{now: now, byKey: map[string]*list.Element{}, order: list.New()}
}

// SessionKey names one caller's conversation. The caller id is part of the key,
// so two API keys naming the same session id never share state.
func SessionKey(callerID, sessionID string) string {
	if sessionID == "" {
		return ""
	}
	return callerID + "\x00" + sessionID
}

// Injected returns the skills this conversation already received in full.
func (s *SkillSessions) Injected(key string) map[string]bool {
	if s == nil || key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.touchLocked(key)
	if e == nil {
		return nil
	}
	out := make(map[string]bool, len(e.skills))
	for id := range e.skills {
		out[id] = true
	}
	return out
}

// Mark records the skills one turn injected in full.
func (s *SkillSessions) Mark(key string, ids []string) {
	if s == nil || key == "" || len(ids) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.touchLocked(key)
	if e == nil {
		e = s.addLocked(key)
	}
	for _, id := range ids {
		if len(e.skills) >= maxSessionSkills {
			break
		}
		e.skills[id] = true
	}
}

// Failover records that a turn moved to another account.
func (s *SkillSessions) Failover(key string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.touchLocked(key)
	if e == nil {
		e = s.addLocked(key)
	}
	if e.failovers < 1<<20 {
		e.failovers++
	}
}

// HadFailover reports whether an earlier turn of this conversation failed over.
func (s *SkillSessions) HadFailover(key string) bool {
	if s == nil || key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.touchLocked(key)
	return e != nil && e.failovers > 0
}

// touchLocked returns a live entry, dropping it when it has expired.
func (s *SkillSessions) touchLocked(key string) *sessionState {
	el, ok := s.byKey[key]
	if !ok {
		return nil
	}
	e := el.Value.(*sessionState)
	now := s.now()
	if now.Sub(e.lastUsed) > sessionLife {
		s.removeLocked(el)
		return nil
	}
	e.lastUsed = now
	s.order.MoveToBack(el)
	return e
}

// addLocked inserts an entry, dropping the expired and then the oldest when the
// table is full.
func (s *SkillSessions) addLocked(key string) *sessionState {
	now := s.now()
	for el := s.order.Front(); el != nil && now.Sub(el.Value.(*sessionState).lastUsed) > sessionLife; el = s.order.Front() {
		s.removeLocked(el)
	}
	for s.order.Len() >= maxSessions {
		s.removeLocked(s.order.Front())
	}
	e := &sessionState{key: key, skills: map[string]bool{}, lastUsed: now}
	s.byKey[key] = s.order.PushBack(e)
	return e
}

func (s *SkillSessions) removeLocked(el *list.Element) {
	delete(s.byKey, el.Value.(*sessionState).key)
	s.order.Remove(el)
}
