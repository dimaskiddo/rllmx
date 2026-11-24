// Package quota decides whether one API key may spend: its stored budgets —
// requests, tokens or cost over a day, month or all time — against the
// usage the analytics store recorded plus the rows still queued for it.
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/ttl"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
)

const (
	// cacheLife is how long every key's budget is cached before a reload.
	cacheLife   = 5 * time.Second
	loadTimeout = 3 * time.Second
	// maxAmount bounds one entry: a larger number is a typo, not a budget.
	maxAmount = 1e15
)

// windows are the periods a budget may name, in evaluation order.
var windows = []string{"day", "month", "total"}

// Amounts is one budget type's amounts, in the windows it limits. Zero means
// that window is not limited.
type Amounts struct {
	Day   float64 `json:"day,omitempty"`
	Month float64 `json:"month,omitempty"`
	Total float64 `json:"total,omitempty"`
}

// Limit is one key's budget. A type or a window left out is not limited.
type Limit struct {
	Requests *Amounts `json:"requests,omitempty"`
	Tokens   *Amounts `json:"tokens,omitempty"`
	Cost     *Amounts `json:"cost,omitempty"`
}

// Validate reads a budget from the dashboard. A wrong type, an unknown field, a
// negative amount or a fraction of a request is refused by name rather than
// repaired, so a client hears what it got wrong.
func Validate(raw gjson.Result) (Limit, error) {
	if !raw.IsObject() {
		return Limit{}, errors.New("quota: must be an object of requests, tokens and cost")
	}
	for name := range raw.Map() {
		if !slices.Contains([]string{"requests", "tokens", "cost"}, name) {
			return Limit{}, errors.New("quota: unexpected field " + name)
		}
	}
	var l Limit
	for _, spec := range []struct {
		name string
		into **Amounts
	}{
		{"requests", &l.Requests}, {"tokens", &l.Tokens}, {"cost", &l.Cost},
	} {
		v := raw.Get(spec.name)
		if !v.Exists() {
			continue
		}
		if !v.IsObject() {
			return Limit{}, fmt.Errorf("quota.%s: must be an object of day, month and total", spec.name)
		}
		for w := range v.Map() {
			if !slices.Contains(windows, w) {
				return Limit{}, fmt.Errorf("quota.%s: unexpected window %s", spec.name, w)
			}
		}
		a := &Amounts{}
		for _, w := range windows {
			n := v.Get(w)
			if !n.Exists() {
				continue
			}
			if n.Type != gjson.Number || n.Float() < 0 || n.Float() > maxAmount {
				return Limit{}, fmt.Errorf("quota.%s.%s: must be a number between 0 and %g", spec.name, w, float64(maxAmount))
			}
			if spec.name != "cost" && n.Float() != float64(int64(n.Float())) {
				return Limit{}, fmt.Errorf("quota.%s.%s: must be a whole number", spec.name, w)
			}
			a.set(w, n.Float())
		}
		if !a.empty() {
			*spec.into = a
		}
	}
	return l, nil
}

// Parse reads a stored budget. An empty object is no budget.
func Parse(raw string) (Limit, error) {
	return Validate(gjson.Parse(raw))
}

// Amounts returns the budget for one type, or the zero value when the type is
// not limited.
func (l Limit) amounts(name string) Amounts {
	switch name {
	case "requests":
		if l.Requests != nil {
			return *l.Requests
		}
	case "tokens":
		if l.Tokens != nil {
			return *l.Tokens
		}
	case "cost":
		if l.Cost != nil {
			return *l.Cost
		}
	}
	return Amounts{}
}

// Empty reports a budget that limits nothing.
func (l Limit) Empty() bool { return l.Requests == nil && l.Tokens == nil && l.Cost == nil }

func (a Amounts) get(window string) float64 {
	switch window {
	case "day":
		return a.Day
	case "month":
		return a.Month
	case "total":
		return a.Total
	}
	return 0
}

func (a *Amounts) set(window string, amount float64) {
	switch window {
	case "day":
		a.Day = amount
	case "month":
		a.Month = amount
	case "total":
		a.Total = amount
	}
}

func (a Amounts) empty() bool { return a.Day == 0 && a.Month == 0 && a.Total == 0 }

// Source is where budgets are read from.
type Source interface {
	All(ctx context.Context) (map[string]Limit, error)
}

// Limits is the budget table over the kv scope, cached for a few seconds; a
// write drops the cache at once.
type Limits struct {
	DB    func() *repository.Store
	Now   func() time.Time
	cache *ttl.Value[map[string]Limit]
}

// NewLimits builds the budget table.
func NewLimits(db func() *repository.Store, now func() time.Time) *Limits {
	if now == nil {
		now = time.Now
	}
	l := &Limits{DB: db, Now: now}
	l.cache = ttl.New(cacheLife, now, l.load)
	return l
}

// All returns every key's budget.
func (l *Limits) All(ctx context.Context) (map[string]Limit, error) {
	return l.cache.Get(ctx, loadTimeout)
}

// Changed drops the cache, so the next check reads what was just written.
func (l *Limits) Changed() { l.cache.Drop() }

// Set stores one key's budget and drops the cache.
func (l *Limits) Set(ctx context.Context, keyID string, limit Limit) error {
	raw, err := json.Marshal(limit)
	if err != nil {
		return err
	}
	if err := l.db().KV().Set(ctx, repository.ScopeKeyLimits, keyID, string(raw)); err != nil {
		return err
	}
	l.Changed()
	return nil
}

// Clear removes a key's budget, keeping no row.
func (l *Limits) Clear(ctx context.Context, keyID string) error {
	return l.Delete(ctx, keyID)
}

// Delete removes a key's budget row.
func (l *Limits) Delete(ctx context.Context, keyID string) error {
	if err := l.db().KV().Delete(ctx, repository.ScopeKeyLimits, keyID); err != nil {
		return err
	}
	l.Changed()
	return nil
}

func (l *Limits) db() *repository.Store {
	if l.DB == nil {
		return nil
	}
	return l.DB()
}

func (l *Limits) load(ctx context.Context) (map[string]Limit, error) {
	db := l.db()
	if db == nil {
		return nil, errors.New("quota: the store is not open")
	}
	rows, err := db.KV().List(ctx, repository.ScopeKeyLimits)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Limit, len(rows))
	for id, raw := range rows {
		limit, err := Parse(raw)
		if err != nil {
			// A stored budget that cannot be read is not treated as no budget: the
			// key fails closed, because a silent clearing would spend unbounded.
			return nil, fmt.Errorf("quota: a stored budget cannot be read: %w", err)
		}
		if !limit.Empty() {
			out[id] = limit
		}
	}
	return out, nil
}

// Remaining is what is left of one window's budgets; -1 means that type is not
// limited there.
type Remaining struct {
	Requests float64 `json:"requests"`
	Tokens   float64 `json:"tokens"`
	Cost     float64 `json:"cost"`
}

// Refusal is the answer to a check: whether the key may spend, and when the
// refused window resets.
type Refusal struct {
	Refused    bool
	RetryAfter time.Duration
	Remaining  Remaining
}

// Quota checks one key's budgets.
type Quota struct {
	Limits  Source
	Usage   func(ctx context.Context, keyID string, since time.Time) (analytics.Totals, error)
	Pending func(keyID string) analytics.Totals
	Now     func() time.Time
}

// Check reports whether a key may make one more request. A key with no budget,
// or an empty id (a loopback caller), passes without a usage query. A read
// failure is returned, never swallowed: the caller fails closed.
func (q *Quota) Check(ctx context.Context, keyID string) (Refusal, error) {
	if keyID == "" || q.Limits == nil || q.Usage == nil {
		return Refusal{}, nil
	}
	limits, err := q.Limits.All(ctx)
	if err != nil {
		return Refusal{}, err
	}
	limit, ok := limits[keyID]
	if !ok {
		return Refusal{}, nil
	}
	now := q.now()
	for _, window := range windows {
		amounts := []struct {
			name   string
			amount Amounts
		}{
			{"requests", limit.amounts("requests")},
			{"tokens", limit.amounts("tokens")},
			{"cost", limit.amounts("cost")},
		}
		anyLimit := false
		for _, a := range amounts {
			if a.amount.get(window) > 0 {
				anyLimit = true
			}
		}
		if !anyLimit {
			continue
		}
		used, err := q.used(ctx, keyID, window, now)
		if err != nil {
			return Refusal{}, err
		}
		for _, a := range amounts {
			amount := a.amount.get(window)
			if amount <= 0 {
				continue
			}
			var got float64
			switch a.name {
			case "requests":
				got = float64(used.Requests)
			case "tokens":
				got = float64(used.Tokens)
			case "cost":
				got = used.Cost
			}
			if got < amount {
				continue
			}
			return Refusal{Refused: true, RetryAfter: untilWindowEnd(window, now), Remaining: left(limit, window, used)}, nil
		}
	}
	return Refusal{}, nil
}

// Standing is how much of one window's budget a key has spent.
type Standing struct {
	// Limited is false when no budget limits this window.
	Limited bool
	// Percent is the largest share spent of any limited type, from 0 to 100.
	Percent float64
	// ResetsAt is when the window begins again; zero for all time.
	ResetsAt time.Time
}

// Standings is a key's standing in each budget window.
type Standings struct {
	Day, Month, Total Standing
}

// Standing reads how far a key is into each of its budget windows, for a client
// that asks how much it has left. A key with no budget reads zero everywhere
// and costs no usage query. A read failure is returned, never read as zero.
func (q *Quota) Standing(ctx context.Context, keyID string) (Standings, error) {
	now := q.now()
	out := Standings{
		Day:   Standing{ResetsAt: windowEnd("day", now)},
		Month: Standing{ResetsAt: windowEnd("month", now)},
	}
	if keyID == "" || q.Limits == nil || q.Usage == nil {
		return out, nil
	}
	limits, err := q.Limits.All(ctx)
	if err != nil {
		return Standings{}, err
	}
	limit, ok := limits[keyID]
	if !ok {
		return out, nil
	}
	for window, into := range map[string]*Standing{"day": &out.Day, "month": &out.Month, "total": &out.Total} {
		share := func(name string, got float64) {
			if amount := limit.amounts(name).get(window); amount > 0 {
				into.Limited = true
				into.Percent = max(into.Percent, min(100, got/amount*100))
			}
		}
		if !limit.windowLimited(window) {
			continue
		}
		used, err := q.used(ctx, keyID, window, now)
		if err != nil {
			return Standings{}, err
		}
		share("requests", float64(used.Requests))
		share("tokens", float64(used.Tokens))
		share("cost", used.Cost)
	}
	return out, nil
}

// windowLimited reports whether any type is limited in a window.
func (l Limit) windowLimited(window string) bool {
	return l.amounts("requests").get(window) > 0 || l.amounts("tokens").get(window) > 0 || l.amounts("cost").get(window) > 0
}

// windowEnd is when a window next begins; zero for all time.
func windowEnd(window string, now time.Time) time.Time {
	if d := untilWindowEnd(window, now); d > 0 {
		return now.Add(d)
	}
	return time.Time{}
}

// Used is the key's spend in every budget window, spent and still queued, for
// the dashboard's read-out. The windows are the ones Check enforces.
func (q *Quota) Used(ctx context.Context, keyID string) (map[string]analytics.Totals, error) {
	out := make(map[string]analytics.Totals, len(windows))
	now := q.now()
	for _, window := range windows {
		got, err := q.used(ctx, keyID, window, now)
		if err != nil {
			return nil, err
		}
		out[window] = got
	}
	return out, nil
}

// used is the key's spend in one window: what the store recorded plus what is
// still queued, so a burst inside one flush is counted.
func (q *Quota) used(ctx context.Context, keyID, window string, now time.Time) (analytics.Totals, error) {
	stored, err := q.Usage(ctx, keyID, windowStart(window, now))
	if err != nil {
		return analytics.Totals{}, err
	}
	if q.Pending != nil {
		p := q.Pending(keyID)
		stored.Requests += p.Requests
		stored.Tokens += p.Tokens
		stored.Cost += p.Cost
	}
	return stored, nil
}

// left is what the refused window has left for each limited type.
func left(limit Limit, window string, used analytics.Totals) Remaining {
	out := Remaining{Requests: -1, Tokens: -1, Cost: -1}
	if amount := limit.amounts("requests").get(window); amount > 0 {
		out.Requests = max(0, amount-float64(used.Requests))
	}
	if amount := limit.amounts("tokens").get(window); amount > 0 {
		out.Tokens = max(0, amount-float64(used.Tokens))
	}
	if amount := limit.amounts("cost").get(window); amount > 0 {
		out.Cost = max(0, amount-used.Cost)
	}
	return out
}

// windowStart is the moment a window began in the clock's own zone, which is the
// server's; the zero time for all time.
func windowStart(window string, now time.Time) time.Time {
	y, m, d := now.Date()
	switch window {
	case "day":
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case "month":
		return time.Date(y, m, 1, 0, 0, 0, 0, now.Location())
	}
	return time.Time{}
}

// untilWindowEnd is how long until a refused window resets; zero for all time.
func untilWindowEnd(window string, now time.Time) time.Duration {
	y, m, d := now.Date()
	switch window {
	case "day":
		return time.Date(y, m, d+1, 0, 0, 0, 0, now.Location()).Sub(now)
	case "month":
		return time.Date(y, m+1, 1, 0, 0, 0, 0, now.Location()).Sub(now)
	}
	return 0
}

func (q *Quota) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}
