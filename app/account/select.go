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

	"rllmx/app/repository"
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
	// Prefer pins the request to one connection while it is a candidate. A pinned
	// connection that is locked, excluded or expired falls back to the strategy.
	Prefer string
}

// Selection is the connection the selector chose.
type Selection struct {
	Conn repository.Connection
	// Skipped counts rows whose stored data could not be read.
	Skipped int
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

// Selector picks a connection for a request. Picking is serialized per
// provider, so the usage counters it writes cannot race inside one process.
type Selector struct {
	conns    ConnReader
	settings *settingsCache
	log      log.Log
	now      func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewSelector returns a Selector.
func NewSelector(o SelectorOptions) *Selector {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Selector{
		conns:    o.Conns,
		settings: &settingsCache{src: o.Settings, now: now},
		log:      o.Log,
		now:      now,
		locks:    map[string]*sync.Mutex{},
	}
}

// InvalidateSettings drops the cached settings, so a change made in this
// process applies to the next selection.
func (s *Selector) InvalidateSettings() { s.settings.drop() }

func (s *Selector) providerLock(p string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[p]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[p] = l
	}
	return l
}

// Select returns the connection that should serve req. It fails with
// ErrNoConnections, an *AllLockedError, or the error of a failed read.
func (s *Selector) Select(ctx context.Context, req Request) (Selection, error) {
	if err := ctx.Err(); err != nil {
		return Selection{}, err
	}
	lock := s.providerLock(req.Provider)
	lock.Lock()
	defer lock.Unlock()

	active := true
	all, err := s.conns.List(ctx, repository.ConnFilter{Provider: req.Provider, IsActive: &active})
	if err != nil {
		return Selection{}, err
	}
	now := s.now()

	var usable, locked []repository.Connection
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
		usable = append(usable, c)
	}

	if len(usable) == 0 {
		if len(locked) > 0 {
			return Selection{Skipped: skipped}, allLocked(locked, req.Model, now)
		}
		if expired > 0 {
			return Selection{Skipped: skipped}, ErrAllExpired
		}
		return Selection{Skipped: skipped}, ErrNoConnections
	}

	if req.Prefer != "" {
		for _, c := range usable {
			if c.ID == req.Prefer {
				return Selection{Conn: c, Skipped: skipped}, nil
			}
		}
	}

	conn, err := s.choose(ctx, usable, req.Provider, now)
	if err != nil {
		return Selection{}, err
	}
	return Selection{Conn: conn, Skipped: skipped}, nil
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

func allLocked(locked []repository.Connection, model string, now time.Time) error {
	var (
		first time.Time
		found bool
		lead  repository.Connection
	)
	for _, c := range locked {
		if until, ok := EarliestLockFor(c.Doc(), model, now); ok && (!found || until.Before(first)) {
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

func (s *Selector) choose(ctx context.Context, usable []repository.Connection, provider string, now time.Time) (repository.Connection, error) {
	settings, err := s.settings.get(ctx)
	if err != nil {
		return repository.Connection{}, err
	}
	if strategyFor(settings, provider) != strategyRound {
		return usable[0], nil
	}
	return s.roundRobin(ctx, usable, stickyLimit(settings, provider), now), nil
}

// roundRobin stays on the most recently used connection until it has served
// limit requests in a row, then moves to the least recently used one. Both the
// counter and the time are stored, so the rotation survives a restart.
func (s *Selector) roundRobin(ctx context.Context, usable []repository.Connection, limit int, now time.Time) repository.Connection {
	current := byLastUse(usable, true)[0]
	count, _ := current.Doc().GetInt("consecutiveUseCount")
	if lastUse(current) != "" && int(count) < limit {
		s.recordUse(ctx, current, true, now)
		return current
	}
	next := byLastUse(usable, false)[0]
	s.recordUse(ctx, next, false, now)
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

// recordUse persists the rotation state. A failed write only costs rotation
// accuracy, so the request is served anyway.
func (s *Selector) recordUse(ctx context.Context, c repository.Connection, sticky bool, now time.Time) {
	if err := s.conns.TouchUsageAt(ctx, c.ID, sticky, useTime(now, c)); err != nil && s.log != nil {
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
