package account

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/store/repository"
)

const codexProvider = "codex"

var (
	// ErrNoConnections means the provider has nothing to select: no connection,
	// all inactive, excluded or expired, or none able to serve the model.
	ErrNoConnections = errors.New("account: no connection available")
	// ErrAllExpired means every connection that could have served was refused
	// for good and has to be connected again. It is an ErrNoConnections too.
	ErrAllExpired = fmt.Errorf("%w: every account needs to be reconnected", ErrNoConnections)
)

// AllLockedError says when the first locked connection frees up. The pipeline
// answers 503 with this as the retry hint.
type AllLockedError struct {
	RetryAt       time.Time
	Human         string
	LastError     string
	LastErrorCode int64
}

func (e *AllLockedError) Error() string {
	return "account: every connection is locked (" + e.Human + ")"
}

// Request asks the selector for one connection.
type Request struct {
	Provider string
	// Model is the model whose lock decides availability.
	Model string
	// RequestedModel is what the client asked for, read by per-account model lists.
	RequestedModel string
	// Exclude lists connections already tried in this request.
	Exclude map[string]bool
	// Hold, when set, names a connection the caller knows is spent for the model
	// and the moment it frees up. A held connection is treated as locked until
	// then, so it is skipped and still counts toward the retry time when every
	// connection is locked. It reads memory only.
	Hold func(connID string) (until time.Time, held bool)
	// Prefer pins the request to one connection while it is a candidate. A pinned
	// connection that is locked, excluded or expired falls back to the strategy.
	Prefer string
}

// Selection is the connection the selector chose.
type Selection struct {
	Conn repository.Connection
	// Skipped counts rows whose stored data could not be read.
	Skipped int
	// More is true when another connection was still usable beside the chosen
	// one, so a failure of this one can be answered by the next.
	More bool
}

// ConnReader is what the selector reads and writes on connections.
type ConnReader interface {
	List(ctx context.Context, f repository.ConnFilter) ([]repository.Connection, error)
	TouchUsageAt(ctx context.Context, id string, sticky bool, at time.Time) error
}

var _ ConnReader = (*repository.ConnectionsRepo)(nil)

// SelectorOptions wires a Selector.
type SelectorOptions struct {
	Conns    ConnReader
	Settings SettingsReader
	Log      log.Log
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Selector picks a connection for a request. Round-robin picking is serialized
// per provider, so the usage counters it writes cannot race inside one process;
// fill-first writes nothing and runs unordered.
type Selector struct {
	conns    ConnReader
	settings *settingsCache
	log      log.Log
	now      func() time.Time

	mu    sync.Mutex
	locks map[string]chan struct{}
}

// NewSelector returns a Selector.
func NewSelector(o SelectorOptions) *Selector {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Selector{
		conns:    o.Conns,
		settings: newSettingsCache(o.Settings, now),
		log:      o.Log,
		now:      now,
		locks:    map[string]chan struct{}{},
	}
}

// Changed drops the cached settings, so a dashboard write such as the rotation
// limit applies to the next pick.
func (s *Selector) Changed() { s.settings.drop() }

// providerLock returns the one-slot semaphore of a provider. A channel, not a
// mutex, so a request that is cancelled while it waits leaves the queue.
func (s *Selector) providerLock(p string) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[p]
	if l == nil {
		l = make(chan struct{}, 1)
		s.locks[p] = l
	}
	return l
}

// needsLock says whether picking writes the rotation counters. Only round-robin
// does; fill-first reads and decides, so concurrent picks need no order. A
// failed settings read takes the lock too, and fails again in choose.
func (s *Selector) needsLock(ctx context.Context, provider string) bool {
	settings, err := s.settings.get(ctx)
	return err != nil || strategyFor(settings, provider) == strategyRound
}

// Select returns the connection that should serve req. It fails with
// ErrNoConnections, an *AllLockedError, or the error of a failed read.
func (s *Selector) Select(ctx context.Context, req Request) (Selection, error) {
	return s.pick(ctx, req, true)
}

// Peek reports the connection Select would choose now, without recording the
// use: a request refused before dispatch spends no account's rotation, but its
// row can still name the account it would have reached.
func (s *Selector) Peek(ctx context.Context, req Request) (Selection, error) {
	return s.pick(ctx, req, false)
}

func (s *Selector) pick(ctx context.Context, req Request, write bool) (Selection, error) {
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	if write && s.needsLock(ctx, req.Provider) {
		lock := s.providerLock(req.Provider)
		select {
		case lock <- struct{}{}:
			defer func() { <-lock }()
		case <-ctx.Done():
			return Selection{}, ctx.Err()
		}
	}

	active := true
	all, err := s.conns.List(ctx, repository.ConnFilter{Provider: req.Provider, IsActive: &active})
	if err != nil {
		return Selection{}, err
	}
	now := s.now()

	var usable, locked []repository.Connection
	heldUntil := map[string]time.Time{}
	skipped, expired := 0, 0
	for _, c := range all {
		if c.Corrupt {
			skipped++
			continue
		}
		if req.Exclude[c.ID] || !ModelEnabled(c, req.RequestedModel) {
			continue
		}
		if Expired(c) {
			expired++
			continue
		}
		if LockActive(c.Doc(), req.Model, now) {
			locked = append(locked, c)
			continue
		}
		if until, held := held(req, c.ID); held && until.After(now) {
			heldUntil[c.ID] = until
			locked = append(locked, c)
			continue
		}
		usable = append(usable, c)
	}

	if len(usable) == 0 {
		if len(locked) > 0 {
			return Selection{Skipped: skipped}, allLocked(locked, req.Model, now, heldUntil)
		}
		if expired > 0 {
			return Selection{Skipped: skipped}, ErrAllExpired
		}
		return Selection{Skipped: skipped}, ErrNoConnections
	}

	if req.Prefer != "" {
		for _, c := range usable {
			if c.ID == req.Prefer {
				return Selection{Conn: c, Skipped: skipped, More: len(usable) > 1}, nil
			}
		}
	}

	conn, err := s.choose(ctx, usable, req.Provider, now, write)
	if err != nil {
		return Selection{}, err
	}
	return Selection{Conn: conn, Skipped: skipped, More: len(usable) > 1}, nil
}

// Expired reports a connection whose refresh token was refused for good.
func Expired(c repository.Connection) bool {
	status, _ := c.Doc().GetString("testStatus")
	return status == repository.StatusExpired
}

// ModelEnabled applies a Codex account's own list of enabled models: an empty
// or missing list allows everything, and so does an empty model.
func ModelEnabled(c repository.Connection, model string) bool {
	if c.Provider != codexProvider || model == "" {
		return true
	}
	raw, ok := c.Doc().GetRaw("providerSpecificData")
	if !ok {
		return true
	}
	list := gjson.Get(raw, "enabledModels")
	if !list.IsArray() || len(list.Array()) == 0 {
		return true
	}
	for _, m := range list.Array() {
		if m.String() == model {
			return true
		}
	}
	return false
}

func allLocked(locked []repository.Connection, model string, now time.Time, held map[string]time.Time) error {
	var (
		first time.Time
		found bool
		lead  repository.Connection
	)
	for _, c := range locked {
		until, ok := EarliestLockFor(c.Doc(), model, now)
		if h, isHeld := held[c.ID]; isHeld && (!ok || h.Before(until)) {
			until, ok = h, true
		}
		if ok && (!found || until.Before(first)) {
			first, found, lead = until, true, c
		}
	}
	if !found {
		return ErrNoConnections
	}
	last, _ := lead.Doc().GetString("lastError")
	code, _ := lead.Doc().GetInt("errorCode")
	return &AllLockedError{RetryAt: first, Human: FormatRetryAfter(first, now), LastError: last, LastErrorCode: code}
}

func (s *Selector) choose(ctx context.Context, usable []repository.Connection, provider string, now time.Time, write bool) (repository.Connection, error) {
	settings, err := s.settings.get(ctx)
	if err != nil {
		return repository.Connection{}, err
	}
	if strategyFor(settings, provider) != strategyRound {
		return usable[0], nil
	}
	return s.roundRobin(ctx, usable, stickyLimit(settings, provider), now, write), nil
}

// roundRobin stays on the most recently used connection until it has served
// limit requests in a row, then moves to the least recently used one. Both the
// counter and the time are stored, so the rotation survives a restart. A peek
// reads the same two fields and writes neither.
func (s *Selector) roundRobin(ctx context.Context, usable []repository.Connection, limit int, now time.Time, write bool) repository.Connection {
	current := byLastUse(usable, true)[0]
	count, _ := current.Doc().GetInt("consecutiveUseCount")
	if lastUse(current) != "" && int(count) < limit {
		if write {
			s.recordUse(ctx, current, true, now)
		}
		return current
	}
	next := byLastUse(usable, false)[0]
	if write {
		s.recordUse(ctx, next, false, now)
	}
	return next
}

func lastUse(c repository.Connection) string {
	v, _ := c.Doc().GetString("lastUsedAt")
	return v
}

// byLastUse orders a copy of conns by last use, most recent first when newest is
// set and least recent first otherwise. A connection never used counts as the
// oldest; ties keep the list's priority order.
func byLastUse(conns []repository.Connection, newest bool) []repository.Connection {
	out := slices.Clone(conns)
	slices.SortStableFunc(out, func(a, b repository.Connection) int {
		ua, ub := lastUse(a), lastUse(b)
		var c int
		switch {
		case ua == "" && ub == "":
			return 0
		case ua == "":
			c = -1
		case ub == "":
			c = 1
		default:
			c = strings.Compare(ua, ub)
		}
		if newest {
			return -c
		}
		return c
	})
	return out
}

// recordUse persists the rotation state. The use happened whether or not the
// caller is still waiting, so the write runs on its own budget. A failed write
// only costs rotation accuracy, and the request is served anyway.
func (s *Selector) recordUse(ctx context.Context, c repository.Connection, sticky bool, now time.Time) {
	wctx, cancel := reportCtx(ctx)
	defer cancel()
	if err := s.conns.TouchUsageAt(wctx, c.ID, sticky, useTime(now, c)); err != nil && s.log != nil {
		s.log.Warningf("account: recording a connection use failed for provider %s: %v", c.Provider, err)
	}
}

// useTime is the time written for c: now, but never at or before the time
// already stored, so two uses within one millisecond still order.
func useTime(now time.Time, c repository.Connection) time.Time {
	if prev, ok := lockUntil(c.Doc(), "lastUsedAt"); ok && !now.After(prev) {
		return prev.Add(time.Millisecond)
	}
	return now
}

// held asks the request's hook about a connection.
func held(req Request, id string) (time.Time, bool) {
	if req.Hold == nil {
		return time.Time{}, false
	}
	return req.Hold(id)
}
