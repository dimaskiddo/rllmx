package quota

import (
	"context"
	"sync"
	"time"
)

// Timing and bounds of the gate. They are the reference's: one fetch per account
// each 30 seconds, and three refusals in a minute block a model for 15 minutes.
const (
	MinRefresh      = 30 * time.Second
	StrikeWindow    = time.Minute
	StrikeThreshold = 3
	StrikeBlock     = 15 * time.Minute
	// maxAccounts bounds every table the gate keeps.
	maxAccounts = 4096
)

// Fetch reads an account's quota from Google. It is given the account's token
// and returns what it could learn; a nil snapshot means nothing was learned.
type Fetch func(ctx context.Context, account, access string) Snapshot

// Gate holds the quota readings of the accounts and passes over one whose model is
// spent. A reading is fetched only after the provider refused a request, at most
// once per account in MinRefresh, with callers that arrive together sharing one
// fetch. A model refused three times in a minute while the reading still shows room
// is blocked for a while anyway, because the reading cannot be trusted then.
type Gate struct {
	Fetch Fetch
	Now   func() time.Time

	mu       sync.Mutex
	readings map[string]Snapshot
	last     map[string]time.Time
	flight   map[string]*flight
	strikes  map[string]strike
	blocks   map[string]time.Time
}

type flight struct {
	done chan struct{}
	snap Snapshot
}

type strike struct {
	count int
	start time.Time
}

// New returns a gate that learns from fetch.
func New(fetch Fetch) *Gate {
	return &Gate{
		Fetch: fetch, Now: time.Now,
		readings: map[string]Snapshot{}, last: map[string]time.Time{}, flight: map[string]*flight{},
		strikes: map[string]strike{}, blocks: map[string]time.Time{},
	}
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func pair(account, model string) string { return account + "|" + cleanModel(model) }

// Skip reports whether the account should be passed over for the model, and until
// when. It reads memory only and never calls Google.
func (g *Gate) Skip(account, model string) (time.Time, bool) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.blocks[pair(account, model)]; ok {
		if until.After(now) {
			return until, true
		}
		delete(g.blocks, pair(account, model))
	}
	if b, ok := g.readings[account].Of(model, now); ok && b.Exhausted(now) {
		return b.ResetAt, true
	}
	return time.Time{}, false
}

// Refused is called when the provider refused a request with a quota status. It
// refreshes the reading when it is allowed to, then answers with the moment the
// account may be tried again for the model: the exact reset when the reading shows
// the model spent, the strike block when three refusals came while it showed room,
// and false when neither applies. The caller falls back to the provider's own hint
// in that case.
func (g *Gate) Refused(ctx context.Context, account, access, model string) (time.Time, bool) {
	snap := g.refresh(ctx, account, access)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	key := pair(account, model)
	b, found := snap.Of(model, now)
	if found && b.Exhausted(now) {
		delete(g.strikes, key)
		g.keep(account, model, b)
		return b.ResetAt, true
	}
	// The reading is missing or optimistic, so count the refusal.
	s := g.strikes[key]
	if s.count == 0 || now.Sub(s.start) > StrikeWindow {
		s = strike{start: now}
	}
	s.count++
	if s.count < StrikeThreshold {
		g.strikes[key] = s
		g.bound()
		return time.Time{}, false
	}
	delete(g.strikes, key)
	until := now.Add(StrikeBlock)
	g.blocks[key] = until
	g.bound()
	return until, true
}

// Succeeded clears the strikes and any block the gate made for the pair, so the
// count means consecutive refusals.
func (g *Gate) Succeeded(account, model string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.strikes, pair(account, model))
	delete(g.blocks, pair(account, model))
}

// Forget drops everything held for an account that was removed.
func (g *Gate) Forget(account string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.readings, account)
	delete(g.last, account)
	for k := range g.strikes {
		if hasAccount(k, account) {
			delete(g.strikes, k)
		}
	}
	for k := range g.blocks {
		if hasAccount(k, account) {
			delete(g.blocks, k)
		}
	}
}

func hasAccount(key, account string) bool {
	return len(key) > len(account) && key[:len(account)] == account && key[len(account)] == '|'
}

// keep records a spent model in the account's reading, so Skip sees it without
// another fetch. The caller holds the lock.
func (g *Gate) keep(account, model string, b Bucket) {
	snap := g.readings[account]
	if snap == nil {
		snap = Snapshot{}
	}
	snap[cleanModel(model)] = b
	g.readings[account] = snap
	g.bound()
}

// refresh fetches the account's reading unless one was made lately; callers that
// arrive while a fetch runs wait for it. It returns the reading in force.
func (g *Gate) refresh(ctx context.Context, account, access string) Snapshot {
	now := g.now()
	g.mu.Lock()
	if f, ok := g.flight[account]; ok {
		g.mu.Unlock()
		select {
		case <-f.done:
			return f.snap
		case <-ctx.Done():
			return nil
		}
	}
	if t, ok := g.last[account]; ok && now.Sub(t) < MinRefresh {
		snap := g.readings[account]
		g.mu.Unlock()
		return snap
	}
	g.last[account] = now
	f := &flight{done: make(chan struct{})}
	g.flight[account] = f
	g.mu.Unlock()

	var fresh Snapshot
	if g.Fetch != nil {
		fresh = g.Fetch(ctx, account, access)
	}

	g.mu.Lock()
	if fresh != nil {
		g.readings[account] = g.withBlocks(account, fresh)
	}
	f.snap = g.readings[account]
	delete(g.flight, account)
	g.bound()
	g.mu.Unlock()
	close(f.done)
	return f.snap
}

// withBlocks lays the live strike blocks over a fresh reading, so an optimistic
// answer cannot lift a block the gate made on purpose. The caller holds the lock.
func (g *Gate) withBlocks(account string, snap Snapshot) Snapshot {
	now := g.now()
	for key, until := range g.blocks {
		if !hasAccount(key, account) || !until.After(now) {
			continue
		}
		snap[key[len(account)+1:]] = Bucket{Remaining: 0, ResetAt: until}
	}
	return snap
}

// bound keeps every table under its limit by dropping the oldest accounts' state.
// The caller holds the lock.
func (g *Gate) bound() {
	for len(g.readings) > maxAccounts {
		for k := range g.readings {
			delete(g.readings, k)
			delete(g.last, k)
			break
		}
	}
	for len(g.last) > maxAccounts {
		for k := range g.last {
			delete(g.last, k)
			break
		}
	}
	for len(g.strikes) > maxAccounts {
		for k := range g.strikes {
			delete(g.strikes, k)
			break
		}
	}
	for len(g.blocks) > maxAccounts {
		for k := range g.blocks {
			delete(g.blocks, k)
			break
		}
	}
}
