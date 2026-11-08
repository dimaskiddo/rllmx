package sso

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Pending limits, from PRD 2: ten thousand sign-ins in flight, ten minutes each.
const (
	maxPending  = 10000
	pendingLife = 10 * time.Minute
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
	// CreatedAt decides expiry.
	CreatedAt time.Time
}

// Pending is the single-use store a sign-in lives in. Take removes the entry, so
// a callback can be replayed only once.
type Pending struct {
	mu      sync.Mutex
	entries map[string]State
	Now     func() time.Time
}

// NewPending builds an empty store.
func NewPending() *Pending {
	return &Pending{entries: map[string]State{}, Now: time.Now}
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
	if len(p.entries) >= maxPending {
		// The store is full of live flows, which means either a burst or an
		// attacker. Refusing the new flow is the safe answer, because dropping
		// another would let its own sign-in fail unpredictably.
		return ErrUnreachable
	}
	p.entries[state] = entry
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

// dropExpired removes every entry past its life. Called under the lock.
func (p *Pending) dropExpired() {
	now := p.Now()
	for k, v := range p.entries {
		if now.Sub(v.CreatedAt) > pendingLife {
			delete(p.entries, k)
		}
	}
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
