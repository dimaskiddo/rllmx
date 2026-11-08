package oauth

import (
	"crypto/subtle"
	"sync"
	"time"

	"rllmx/app/sso"
)

// Limits of the login store. A login must finish on the instance that started
// it, so the store is a plain in-process map and never the shared cache.
const (
	sessionLife = 10 * time.Minute
	doneLife    = 2 * time.Minute
	maxSessions = 1000
	maxPerBind  = 20
)

// Session is one login in flight. The verifier never leaves this process.
type Session struct {
	Provider string
	Redirect string
	Verifier string
	// Port is the listener port that serves this login, or 0.
	Port int
	// Bind ties the login to the dashboard session that started it.
	Bind string
}

type phase int

const (
	pending phase = iota + 1
	busy
	finished
)

type entry struct {
	Session
	phase   phase
	conn    string
	code    string
	created time.Time
	expires time.Time
}

// Sessions is the store of logins. Begin hands a login out once, so a callback
// cannot be replayed; the outcome is kept briefly and read once.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]*entry
	Now func() time.Time
}

// NewSessions builds an empty store.
func NewSessions() *Sessions { return &Sessions{m: map[string]*entry{}, Now: time.Now} }

// SameBind compares two bindings in constant time; an empty one never matches.
func SameBind(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Sessions) sweep(now time.Time) {
	for k, e := range s.m {
		if now.After(e.expires) {
			delete(s.m, k)
		}
	}
}

// Put stores a login under a fresh 256-bit state. A binding that already has
// many logins in flight loses its oldest.
func (s *Sessions) Put(sess Session) (string, error) {
	state, err := sso.Nonce()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	s.sweep(now)
	if len(s.m) >= maxSessions {
		return "", ErrSession
	}
	var oldest string
	own := 0
	for k, e := range s.m {
		if e.Bind != sess.Bind {
			continue
		}
		own++
		if oldest == "" || e.created.Before(s.m[oldest].created) {
			oldest = k
		}
	}
	if own >= maxPerBind {
		delete(s.m, oldest)
	}
	s.m[state] = &entry{Session: sess, phase: pending, created: now, expires: now.Add(sessionLife)}
	return state, nil
}

// Begin takes a pending login if allow accepts it. The login is then busy, so
// a second Begin of the same state fails whatever the first one did.
func (s *Sessions) Begin(state string, allow func(Session) bool) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(s.Now())
	e, ok := s.m[state]
	if !ok || e.phase != pending || !allow(e.Session) {
		return Session{}, false
	}
	e.phase = busy
	return e.Session, true
}

// End records the outcome of a busy login: the connection id, or a short code.
func (s *Sessions) End(state, connID, code string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[state]
	if !ok {
		return
	}
	e.phase, e.conn, e.code = finished, connID, code
	e.expires = s.Now().Add(doneLife)
}

// Peek reports the listener port of a pending login.
func (s *Sessions) Peek(state string) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(s.Now())
	e, ok := s.m[state]
	if !ok || e.phase != pending {
		return 0, false
	}
	return e.Port, true
}

// Waiting counts the logins that still wait for the browser on a port.
func (s *Sessions) Waiting(port int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(s.Now())
	n := 0
	for _, e := range s.m {
		if e.Port == port && e.phase == pending {
			n++
		}
	}
	return n
}

// Status is what the dashboard learns about a login.
type Status struct {
	State        string `json:"status"`
	ConnectionID string `json:"connectionId,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Status reports a login to the browser that started it. A finished login is
// read once. Anything the caller may not see is "unknown".
func (s *Sessions) Status(state, bind string) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(s.Now())
	e, ok := s.m[state]
	if !ok || !SameBind(e.Bind, bind) {
		return Status{State: "unknown"}
	}
	if e.phase != finished {
		return Status{State: "pending"}
	}
	delete(s.m, state)
	if e.code != "" {
		return Status{State: "error", Error: e.code}
	}
	return Status{State: "done", ConnectionID: e.conn}
}
