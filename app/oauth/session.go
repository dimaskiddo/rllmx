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
	// DeviceCode, Interval and Life belong to a device login. The code never
	// leaves this process; Interval is the pause the provider asked for between
	// two polls, and Life how long the code stays valid.
	DeviceCode string
	Interval   time.Duration
	Life       time.Duration
	// DeviceID is the id made for the account when the login started. It goes
	// on the provider's calls and is stored with the account; the browser never
	// sees it.
	DeviceID string
	// DeviceExtra is what a device dialect kept for its poll: a mode flag, a
	// one-off poll token. The browser never sees it.
	DeviceExtra string
	// App is the OAuth application that came with the request, for a login
	// whose data asks for one. It lives here and in the stored account only.
	App *App
	// Variant is which of a provider's several logins this is.
	Variant string
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
	// next is the earliest time the provider may be asked again.
	next time.Time
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
	life := sessionLife
	if sess.Life > 0 {
		life = sess.Life
	}
	s.m[state] = &entry{Session: sess, phase: pending, created: now, expires: now.Add(life), next: now.Add(sess.Interval)}
	return state, nil
}

// Gate is what a poll learns before it may call the provider.
type Gate int

const (
	// GateUnknown: no such login, a login that is not this browser's, or one that
	// has finished, whose outcome the status read hands out.
	GateUnknown Gate = iota
	// GateWait: the provider asked for more time between polls.
	GateWait
	// GateGo: the caller holds the login and must call Rest or End.
	GateGo
)

// Poll hands a pending device login to one caller, but not before the pause the
// provider asked for has passed. The login is busy until Rest or End, so two
// simultaneous polls make one provider call.
func (s *Sessions) Poll(state string, allow func(Session) bool) (Session, Gate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	s.sweep(now)
	e, ok := s.m[state]
	if !ok || e.DeviceCode == "" || e.phase == finished || !allow(e.Session) {
		return Session{}, GateUnknown
	}
	if e.phase != pending || now.Before(e.next) {
		return Session{}, GateWait
	}
	e.phase = busy
	return e.Session, GateGo
}

// Rest gives a device login back after a poll that found it still waiting, and
// sets when it may be asked again.
func (s *Sessions) Rest(state string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[state]
	if !ok || e.phase != busy {
		return
	}
	e.phase, e.Interval, e.next = pending, interval, s.Now().Add(interval)
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
