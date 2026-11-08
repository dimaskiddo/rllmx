// Package conversation says which conversation a request belongs to. The id is
// read from what the client sent, or derived from the conversation itself, or
// made up per connection; it is bound to the connection by a keyed hash before
// any upstream sees it.
package conversation

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Caps and lifetimes of the in-process tables.
const (
	maxDerived = 5000
	maxMadeUp  = 1000
	// Life is how long a derived or made-up id lasts without use.
	Life = 2 * time.Hour
)

type entry struct {
	id       string
	lastUsed time.Time
}

// Store holds the ids the program derives or makes up, and the key that binds
// an id to a connection. It is safe for concurrent use.
type Store struct {
	now func() time.Time
	key [32]byte

	mu      sync.Mutex
	derived map[string]*entry
	madeUp  map[string]*entry
}

// NewStore returns a Store. now is the clock; nil means time.Now. The binding
// key is random per process, so a bound value cannot be predicted or replayed
// from another run.
func NewStore(now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	s := &Store{now: now, derived: map[string]*entry{}, madeUp: map[string]*entry{}}
	if _, err := rand.Read(s.key[:]); err != nil {
		panic("conversation: no randomness: " + err.Error())
	}
	return s
}

// Bound returns the value to send upstream for a session: a keyed hash of the
// session id and the connection. A value the client chose is never sent as it
// is, so one tenant cannot pick the cache key another tenant's conversation
// uses.
func (s *Store) Bound(sessionID, connectionID string) string {
	m := hmac.New(sha256.New, s.key[:])
	m.Write([]byte(connectionID))
	m.Write([]byte{0})
	m.Write([]byte(sessionID))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// lookup returns the id stored under key, refreshing it, or false when it is
// absent or has expired.
func (s *Store) lookup(table map[string]*entry, key string) (string, bool) {
	e, ok := table[key]
	if !ok {
		return "", false
	}
	now := s.now()
	if now.Sub(e.lastUsed) > Life {
		delete(table, key)
		return "", false
	}
	e.lastUsed = now
	return e.id, true
}

// store adds an id, dropping expired entries first and then the oldest one when
// the table is still full.
func (s *Store) store(table map[string]*entry, limit int, key, id string) {
	now := s.now()
	if len(table) >= limit {
		var oldestKey string
		var oldest time.Time
		for k, e := range table {
			if now.Sub(e.lastUsed) > Life {
				delete(table, k)
				continue
			}
			if oldestKey == "" || e.lastUsed.Before(oldest) {
				oldestKey, oldest = k, e.lastUsed
			}
		}
		if len(table) >= limit && oldestKey != "" {
			delete(table, oldestKey)
		}
	}
	table[key] = &entry{id: id, lastUsed: now}
}

// madeUpID is a random id plus the time in milliseconds.
func (s *Store) madeUpID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("conversation: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:]) + "-" + itoa(s.now().UnixMilli())
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
