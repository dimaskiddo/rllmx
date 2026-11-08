package sso

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Pending limits: ten thousand sign-ins in flight, ten minutes each.
const (
	maxPending  = 10000
	pendingLife = 10 * time.Minute
	// maxPerOwner bounds the flows one client address may hold open, so one
	// caller cannot fill the store and lock everyone else out of sign-in.
	maxPerOwner = 50
	// stateBytes is how much randomness a flow state carries.
	stateBytes = 32
)

// State is one sign-in in flight. It never leaves this process: a sign-in must
// finish on the instance that started it, so this store is deliberately not the
// shared cache.
type State struct {
	// Nonce is echoed inside the ID token and compared on the way back.
	Nonce string
	// Verifier is the PKCE secret; only its challenge travels to the provider.
	Verifier string
	// Issuer and ClientID are the settings in force when the flow started, so a
	// settings change mid-flow cannot redirect it to another provider.
	Issuer   string
	ClientID string
	// Owner is the client the flow belongs to, empty when the caller has none.
	Owner string
	// CreatedAt decides expiry.
	CreatedAt time.Time
}

type queued struct {
	key string
	at  time.Time
}

// Pending is the single-use store a sign-in lives in. Take removes the entry, so
// a callback can be replayed only once.
type Pending struct {
	mu      sync.Mutex
	entries map[string]State
	// queue holds keys in creation order, so expiry pops from the front. A key
	// already taken stays here until it ages out or the queue is compacted.
	queue  []queued
	owners map[string]int
	Now    func() time.Time
}

// NewPending builds an empty store.
func NewPending() *Pending {
	return &Pending{entries: map[string]State{}, owners: map[string]int{}, Now: time.Now}
}

// Put stores a sign-in under a fresh state and returns the state.
func (p *Pending) Put(entry State) (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}
	return state, p.PutKeyed(state, entry)
}

// PutKeyed stores a sign-in under a key the caller chose. The SAML relay state is
// the request id, so that flow needs to name its own key rather than having one
// minted.
func (p *Pending) PutKeyed(state string, entry State) error {
	if state == "" {
		return ErrUnreachable
	}
	entry.CreatedAt = p.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropExpired()
	if len(p.entries) >= maxPending || (entry.Owner != "" && p.owners[entry.Owner] >= maxPerOwner) {
		// The store is full of live flows, or this client holds its share:
		// either a burst or an attacker. Refusing the new flow is the safe
		// answer, because dropping another would let its own sign-in fail
		// unpredictably.
		return ErrUnreachable
	}
	if old, ok := p.entries[state]; ok {
		p.release(old)
	}
	p.entries[state] = entry
	if entry.Owner != "" {
		p.owners[entry.Owner]++
	}
	p.queue = append(p.queue, queued{key: state, at: entry.CreatedAt})
	if len(p.queue) > 2*maxPending {
		p.compact()
	}
	return nil
}

// Take removes and returns the sign-in for a state. ok is false when the state
// is unknown, expired or already used.
func (p *Pending) Take(state string) (State, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[state]
	if !ok {
		return State{}, false
	}
	delete(p.entries, state)
	p.release(entry)
	if p.Now().Sub(entry.CreatedAt) > pendingLife {
		return State{}, false
	}
	return entry, true
}

// Len reports how many sign-ins are waiting, for a test.
func (p *Pending) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// release gives back the per-owner slot an entry held. Called under the lock.
func (p *Pending) release(e State) {
	if e.Owner == "" {
		return
	}
	if p.owners[e.Owner] <= 1 {
		delete(p.owners, e.Owner)
		return
	}
	p.owners[e.Owner]--
}

// dropExpired removes the entries past their life from the front of the queue.
// Called under the lock.
func (p *Pending) dropExpired() {
	now := p.Now()
	n := 0
	for n < len(p.queue) && now.Sub(p.queue[n].at) > pendingLife {
		q := p.queue[n]
		if e, ok := p.entries[q.key]; ok && e.CreatedAt.Equal(q.at) {
			delete(p.entries, q.key)
			p.release(e)
		}
		n++
	}
	p.queue = p.queue[n:]
}

// compact drops queue slots whose entry was taken, so a client that starts and
// finishes flows cannot grow the queue. Called under the lock.
func (p *Pending) compact() {
	live := p.queue[:0]
	for _, q := range p.queue {
		if e, ok := p.entries[q.key]; ok && e.CreatedAt.Equal(q.at) {
			live = append(live, q)
		}
	}
	p.queue = live
}

// randomState returns 32 random bytes in unpadded base64url.
func randomState() (string, error) {
	b := make([]byte, stateBytes)
	if _, err := rand.Read(b); err != nil {
		return "", ErrUnreachable
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Nonce returns 32 random bytes in unpadded base64url, for the ID token to echo.
func Nonce() (string, error) { return randomState() }
