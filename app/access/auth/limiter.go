package auth

import (
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	// FreeAttempts is how many failures a key may have before the first lock.
	FreeAttempts = 5
	// quietAfter is how long without a failure resets the whole history.
	quietAfter = time.Hour
	// maxEntries caps the in-process store. Past it the eldest entry goes, which
	// can free a locked address: the cap protects the process, and a Redis copy
	// still holds the lock where one is configured.
	maxEntries = 10000
)

// lockSteps is the escalation after each burst of FreeAttempts failures.
var lockSteps = [...]time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

// Limiter is the progressive login lockout. It keeps its own store rather than
// using the shared cache, because the cache evicts across groups and a lock that
// silently vanished would hand out free guesses.
type Limiter struct {
	mu      sync.Mutex
	entries map[string]*attempts
}

type attempts struct {
	fails     int
	level     int
	lockUntil time.Time
	lastFail  time.Time
}

// NewLimiter builds an empty limiter.
func NewLimiter() *Limiter { return &Limiter{entries: map[string]*attempts{}} }

// Key normalises a client address for the limiter: an IPv6 address becomes its
// /64, because one host usually owns a whole prefix and rotating within it would
// otherwise buy a fresh budget per address.
func Key(addr string) string {
	addr = strings.TrimSpace(addr)
	host := addr
	if ap, err := netip.ParseAddrPort(addr); err == nil {
		host = ap.Addr().String()
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return addr
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	prefix, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return prefix.String()
}

// Locked reports the remaining lock for a key, or zero when it may try.
func (l *Limiter) Locked(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	e := l.live(key, now)
	if e == nil || e.lockUntil.IsZero() {
		return 0
	}
	if remaining := e.lockUntil.Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

// Fail records an attempt and returns how many tries are left before the next
// lock, and the lock that just started if this attempt triggered one.
func (l *Limiter) Fail(key string, now time.Time) (left int, locked time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e := l.live(key, now)
	if e == nil {
		e = &attempts{}
		l.entries[key] = e
		if len(l.entries) > maxEntries {
			l.evict()
		}
	}
	e.fails++
	e.lastFail = now
	if e.fails >= FreeAttempts {
		e.lockUntil = now.Add(lockSteps[min(e.level, len(lockSteps)-1)])
		e.level++
		e.fails = 0
		return 0, lockSteps[min(e.level-1, len(lockSteps)-1)]
	}
	return FreeAttempts - e.fails, 0
}

// Succeed clears the history for a key: a correct password wipes both the
// failure count and the escalation.
func (l *Limiter) Succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// live returns the entry while it still applies, dropping one whose quiet hour
// has passed and that is not currently locked.
func (l *Limiter) live(key string, now time.Time) *attempts {
	e, ok := l.entries[key]
	if !ok {
		return nil
	}
	locked := e.lockUntil.After(now)
	if !locked && now.Sub(e.lastFail) > quietAfter {
		delete(l.entries, key)
		return nil
	}
	return e
}

// evict drops the entry that has been idle longest. The map is small enough at
// the cap for the linear scan to cost nothing next to a bcrypt comparison.
func (l *Limiter) evict() {
	var oldest string
	var when time.Time
	for k, e := range l.entries {
		if oldest == "" || e.lastFail.Before(when) {
			oldest, when = k, e.lastFail
		}
	}
	delete(l.entries, oldest)
}
