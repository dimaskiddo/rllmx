package controllers

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/access/auth"
	"rllmx/app/gateway/health"
	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
)

const (
	usageRecentShow = 20
	usageLogRows    = 200
	usageLastSeen   = 48 * time.Hour
	usageDayLayout  = "2006-01-02"
)

var (
	usageDays = map[string]int{"7d": 7, "30d": 30, "60d": 60}
	// errUsageStore is answered with the route's own 500 string.
	errUsageStore = errors.New("usage: the analytics store is not open")
)

// UsageReader is the part of the analytics store the usage pages read. keys
// narrows an answer to a standard user's keys; nil means every key.
type UsageReader interface {
	Sums(ctx context.Context, from time.Time, hourly bool, keys []string) ([]analytics.Sum, error)
	Buckets(ctx context.Context, from time.Time, hourly bool, keys []string) ([]analytics.Bucket, error)
	LastSeen(ctx context.Context, since time.Time, keys []string) (map[analytics.Dims]time.Time, error)
	Recent(ctx context.Context, limit int, keys []string) ([]analytics.Request, error)
	RecentServed(ctx context.Context, limit int, keys []string) ([]analytics.Request, error)
	Providers(ctx context.Context, keys []string) ([]string, error)
}

// UsageController serves the dashboard's usage pages over the analytics store
// and the live in-flight table.
type UsageController struct {
	Analytics func() UsageReader
	// Flushed counts the usage batches written so far; the feed resends the
	// recent list when it moves.
	Flushed  func() uint64
	DB       func() *repository.Store
	Registry *upstream.Registry
	Inflight *health.Inflight
	// Quota answers the per-connection reads and the reset routes; nil when no
	// reader is wired.
	Quota func() QuotaReader
	Log   log.Log
	// Users holds the standard-user accounts, so a user's read is narrowed to
	// the keys assigned to them. Nil leaves every read unscoped.
	Users *auth.Users
	// Loc and Now pin the clock in a test; nil reads time.Local and time.Now.
	Loc         *time.Location
	Now         func() time.Time
	every, beat time.Duration
}

// usageTotals is the counter set every usage map entry carries.
type usageTotals struct {
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	Cost             float64 `json:"cost"`
}

type usageModelEntry struct {
	usageTotals
	RawModel string `json:"rawModel"`
	Provider string `json:"provider"`
	LastUsed string `json:"lastUsed"`
}

type usageAccountEntry struct {
	usageTotals
	RawModel     string `json:"rawModel"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connectionId"`
	AccountName  string `json:"accountName"`
	LastUsed     string `json:"lastUsed"`
}

type usageKeyEntry struct {
	usageTotals
	RawModel     string `json:"rawModel"`
	Provider     string `json:"provider"`
	APIKeyMasked string `json:"apiKeyMasked"`
	KeyName      string `json:"keyName"`
	APIKeyKey    string `json:"apiKeyKey"`
	LastUsed     string `json:"lastUsed"`
}

type usageEndpointEntry struct {
	usageTotals
	Endpoint string `json:"endpoint"`
	RawModel string `json:"rawModel"`
	Provider string `json:"provider"`
	LastUsed string `json:"lastUsed"`
}

type usagePending struct {
	ByModel   map[string]int            `json:"byModel"`
	ByAccount map[string]map[string]int `json:"byAccount"`
}

type usageActive struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
}

type usageRecent struct {
	Timestamp        string `json:"timestamp"`
	Model            string `json:"model"`
	Provider         string `json:"provider"`
	PromptTokens     int64  `json:"promptTokens"`
	CompletionTokens int64  `json:"completionTokens"`
	CachedTokens     int64  `json:"cachedTokens"`
	Status           string `json:"status"`
}

type usageStats struct {
	TotalRequests         int64                         `json:"totalRequests"`
	TotalPromptTokens     int64                         `json:"totalPromptTokens"`
	TotalCompletionTokens int64                         `json:"totalCompletionTokens"`
	TotalCachedTokens     int64                         `json:"totalCachedTokens"`
	TotalCost             float64                       `json:"totalCost"`
	ByProvider            map[string]usageTotals        `json:"byProvider"`
	ByModel               map[string]usageModelEntry    `json:"byModel"`
	ByAccount             map[string]usageAccountEntry  `json:"byAccount"`
	ByAPIKey              map[string]usageKeyEntry      `json:"byApiKey"`
	ByEndpoint            map[string]usageEndpointEntry `json:"byEndpoint"`
	Pending               usagePending                  `json:"pending"`
	ActiveRequests        []usageActive                 `json:"activeRequests"`
	RecentRequests        []usageRecent                 `json:"recentRequests"`
	ErrorProvider         string                        `json:"errorProvider"`
}

type usageChartPoint struct {
	Label    string  `json:"label"`
	Tokens   int64   `json:"tokens"`
	Cost     float64 `json:"cost"`
	Requests int64   `json:"requests"`
}

// usageWindow is one period resolved to the moment it starts and the rollup it
// reads.
type usageWindow struct {
	key   string
	from  time.Time
	hours bool
}

func (c *UsageController) loc() *time.Location {
	if c.Loc != nil {
		return c.Loc
	}
	return time.Local
}

func (c *UsageController) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// midnight is the start of today in the reporting zone.
func (c *UsageController) midnight() time.Time {
	n := c.now().In(c.loc())
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, c.loc())
}

func (c *UsageController) window(period string) (usageWindow, bool) {
	switch period {
	case "today":
		return usageWindow{key: period, from: c.midnight(), hours: true}, true
	case "24h":
		return usageWindow{key: period, from: c.now().UTC().Truncate(time.Hour).Add(-23 * time.Hour), hours: true}, true
	case "7d", "30d", "60d":
		return usageWindow{key: period, from: c.midnight().AddDate(0, 0, -(usageDays[period] - 1))}, true
	case "all":
		return usageWindow{key: period}, true
	}
	return usageWindow{}, false
}

func (c *UsageController) reader() UsageReader {
	if c.Analytics == nil {
		return nil
	}
	return c.Analytics()
}

// scope is the key ids a standard user may read, or nil for the admin. A user
// with no keys reads zero, never everything.
func (c *UsageController) scope(ctx contractshttp.Context) ([]string, contractshttp.Response, bool) {
	who, ok := apphttp.SessionFrom(ctx)
	if !ok || who.Role != auth.RoleUser {
		return nil, nil, true
	}
	if c.Users == nil {
		return []string{}, nil, true
	}
	keys, err := c.Users.KeysOf(ctx.Request().Origin().Context(), who.UserID)
	if err != nil {
		return nil, storageDown(ctx), false
	}
	return keys, nil, true
}

// requestedPeriod reads the period, defaulting to fallback.
func requestedPeriod(ctx contractshttp.Context, fallback string) string {
	if p := strings.TrimSpace(ctx.Request().Query("period")); p != "" {
		return p
	}
	return fallback
}

func (c *UsageController) failed(ctx contractshttp.Context, route string, msg string, err error) contractshttp.Response {
	if c.Log != nil && err != nil {
		c.Log.Warningf("usage: %s: %v", route, err)
	}
	return flat(ctx, nethttp.StatusInternalServerError, msg)
}

// Stats answers GET /api/usage/stats over the requested period.
func (c *UsageController) Stats(ctx contractshttp.Context) contractshttp.Response {
	return c.stats(ctx, requestedPeriod(ctx, "7d"), "Failed to fetch usage stats")
}

// History answers GET /api/usage/history with the all-time totals.
func (c *UsageController) History(ctx contractshttp.Context) contractshttp.Response {
	return c.stats(ctx, "all", "Failed to fetch usage stats")
}

func (c *UsageController) stats(ctx contractshttp.Context, period, msg string) contractshttp.Response {
	keys, out3, ok := c.scope(ctx)
	if !ok {
		return out3
	}
	w, ok := c.window(period)
	if !ok {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid period")
	}
	out, err := c.build(ctx.Request().Origin().Context(), w, keys)
	if err != nil {
		return c.failed(ctx, "stats", msg, err)
	}
	return ctx.Response().Json(nethttp.StatusOK, out)
}

// Chart answers GET /api/usage/chart with one point per hour or day.
func (c *UsageController) Chart(ctx contractshttp.Context) contractshttp.Response {
	keys, out3, ok := c.scope(ctx)
	if !ok {
		return out3
	}
	w, ok := c.window(requestedPeriod(ctx, "7d"))
	if !ok {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid period")
	}
	out, err := c.chart(ctx.Request().Origin().Context(), w, keys)
	if err != nil {
		return c.failed(ctx, "chart", "Failed to fetch chart data", err)
	}
	return ctx.Response().Json(nethttp.StatusOK, out)
}

// Logs answers GET /api/usage/logs with the newest rows as one line each.
func (c *UsageController) Logs(ctx contractshttp.Context) contractshttp.Response {
	st := c.reader()
	if st == nil {
		return c.failed(ctx, "logs", "Failed to fetch logs", errUsageStore)
	}
	keys, out3, ok := c.scope(ctx)
	if !ok {
		return out3
	}
	rc := ctx.Request().Origin().Context()
	rows, err := st.Recent(rc, usageLogRows, keys)
	if err != nil {
		return c.failed(ctx, "logs", "Failed to fetch logs", err)
	}
	accounts := c.accountNames(rc)
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, usageLogLine(r, accounts[r.ConnectionID], c.loc()))
	}
	return ctx.Response().Json(nethttp.StatusOK, lines)
}

// Providers answers GET /api/usage/providers with every provider the store has
// served, for the filter.
func (c *UsageController) Providers(ctx contractshttp.Context) contractshttp.Response {
	st := c.reader()
	if st == nil {
		return c.failed(ctx, "providers", "Failed to fetch providers", errUsageStore)
	}
	rc := ctx.Request().Origin().Context()
	keys, out3, ok := c.scope(ctx)
	if !ok {
		return out3
	}
	ids, err := st.Providers(rc, keys)
	if err != nil {
		return c.failed(ctx, "providers", "Failed to fetch providers", err)
	}
	nodes := nodeNames(rc, c.store())
	out := make([]contractshttp.Json, 0, len(ids))
	for _, id := range ids {
		out = append(out, contractshttp.Json{"id": id, "name": c.providerName(id, nodes)})
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"providers": out})
}

// ActiveProviders answers GET /api/usage/active-providers: how many providers
// have a request running now. The period is validated and echoed, not read.
func (c *UsageController) ActiveProviders(ctx contractshttp.Context) contractshttp.Response {
	period := requestedPeriod(ctx, "today")
	if _, ok := c.window(period); !ok {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid period")
	}
	seen := map[string]bool{}
	for _, a := range c.Inflight.Active() {
		seen[a.Provider] = true
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"period": period, "activeProviders": len(seen)})
}

func (c *UsageController) build(ctx context.Context, w usageWindow, keys []string) (usageStats, error) {
	st := c.reader()
	if st == nil {
		return usageStats{}, errUsageStore
	}
	sums, err := st.Sums(ctx, w.from, w.hours, keys)
	if err != nil {
		return usageStats{}, err
	}
	seen, err := st.LastSeen(ctx, c.now().Add(-usageLastSeen), keys)
	if err != nil {
		return usageStats{}, err
	}
	nodes := nodeNames(ctx, c.store())
	accounts := c.accountNames(ctx)
	names := usageNames{accounts: accounts, keys: c.keyNames(ctx)}

	out := usageStats{
		ByProvider:     map[string]usageTotals{},
		ByModel:        map[string]usageModelEntry{},
		ByAccount:      map[string]usageAccountEntry{},
		ByAPIKey:       map[string]usageKeyEntry{},
		ByEndpoint:     map[string]usageEndpointEntry{},
		ActiveRequests: []usageActive{},
		RecentRequests: []usageRecent{},
	}
	for _, s := range sums {
		last := s.Last
		if at, ok := seen[s.Dims]; ok && at.After(last) {
			last = at
		}
		names.provider = c.providerName(s.Provider, nodes)
		out.add(s, names, last.UTC().Format("2006-01-02T15:04:05.000Z"))
	}

	// The in-flight table holds no key id, so a standard user's page omits it
	// rather than showing every account's live requests.
	if keys == nil {
		out.Pending, out.ActiveRequests = c.live(accounts)
		out.ErrorProvider = c.Inflight.ErrorProvider()
	}

	recent, err := c.recent(ctx, keys)
	if err != nil {
		return usageStats{}, err
	}
	out.RecentRequests = recent
	return out, nil
}

// usageNames are the display names one sum is filed under.
type usageNames struct {
	accounts map[string]string
	keys     map[string]usageKeyName
	// provider is the current sum's provider, as the page names it.
	provider string
}

// add files one sum under each of the page's groupings. stamp is when the sum
// was last used.
func (u *usageStats) add(s analytics.Sum, n usageNames, stamp string) {
	t := usageTotals{Requests: s.Requests, PromptTokens: s.Prompt, CompletionTokens: s.Completion, CachedTokens: s.Cached, Cost: s.Cost}
	provider := n.provider

	v := u.ByProvider[s.Provider]
	v.add(t)
	u.ByProvider[s.Provider] = v
	u.TotalRequests += t.Requests
	u.TotalPromptTokens += t.PromptTokens
	u.TotalCompletionTokens += t.CompletionTokens
	u.TotalCachedTokens += t.CachedTokens
	u.TotalCost += t.Cost

	mk := s.Model + " (" + s.Provider + ")"
	m := u.ByModel[mk]
	m.usageTotals.add(t)
	m.RawModel, m.Provider, m.LastUsed = s.Model, provider, stamp
	u.ByModel[mk] = m

	name := n.accounts[s.ConnectionID]
	ak := s.Model + " (" + provider + " - " + name + ")"
	a := u.ByAccount[ak]
	a.usageTotals.add(t)
	a.RawModel, a.Provider, a.ConnectionID, a.AccountName, a.LastUsed = s.Model, provider, s.ConnectionID, name, stamp
	u.ByAccount[ak] = a

	k := usageKeyName{}
	if stored, ok := n.keys[s.APIKeyID]; ok {
		k = stored
	} else if s.APIKeyID != "" {
		short := text.CutBytes(s.APIKeyID, 8)
		k = usageKeyName{name: short, masked: short}
	}
	keyKey := "local-no-key"
	if s.APIKeyID != "" {
		keyKey = s.APIKeyID
	} else {
		k = usageKeyName{name: "Local (No API Key)"}
	}
	kk := keyKey + "|" + s.Model + "|" + providerOr(s.Provider)
	ke := u.ByAPIKey[kk]
	ke.usageTotals.add(t)
	ke.RawModel, ke.Provider, ke.LastUsed = s.Model, provider, stamp
	ke.APIKeyKey, ke.APIKeyMasked, ke.KeyName = keyKey, k.masked, k.name
	u.ByAPIKey[kk] = ke

	ek := s.Endpoint + "|" + s.Model + "|" + providerOr(s.Provider)
	e := u.ByEndpoint[ek]
	e.usageTotals.add(t)
	e.Endpoint, e.RawModel, e.Provider, e.LastUsed = s.Endpoint, s.Model, provider, stamp
	u.ByEndpoint[ek] = e
}

// recent is the newest finished rows the page shows, newest first.
func (c *UsageController) recent(ctx context.Context, keys []string) ([]usageRecent, error) {
	st := c.reader()
	if st == nil {
		return []usageRecent{}, errUsageStore
	}
	rows, err := st.RecentServed(ctx, usageRecentShow, keys)
	if err != nil {
		return nil, err
	}
	out := make([]usageRecent, 0, len(rows))
	for _, r := range rows {
		out = append(out, usageRecent{
			Timestamp: r.Time.UTC().Format("2006-01-02T15:04:05.000Z"), Model: r.Model,
			Provider: r.Provider, PromptTokens: r.Prompt, CompletionTokens: r.Completion,
			CachedTokens: r.Cached, Status: usageStatus(r),
		})
	}
	return out, nil
}

func (c *UsageController) live(accounts map[string]string) (usagePending, []usageActive) {
	pending := usagePending{ByModel: map[string]int{}, ByAccount: map[string]map[string]int{}}
	active := []usageActive{}
	for _, s := range c.Inflight.Active() {
		key := s.Model + " (" + s.Provider + ")"
		pending.ByModel[key] += s.Count
		if pending.ByAccount[s.ConnectionID] == nil {
			pending.ByAccount[s.ConnectionID] = map[string]int{}
		}
		pending.ByAccount[s.ConnectionID][key] += s.Count
		name := accounts[s.ConnectionID]
		if name == "" && s.ConnectionID != "" {
			name = connectionAccount(repository.Connection{ID: s.ConnectionID})
		}
		active = append(active, usageActive{Model: s.Model, Provider: s.Provider, Account: name, Count: s.Count})
	}
	return pending, active
}

func (c *UsageController) chart(ctx context.Context, w usageWindow, keys []string) ([]usageChartPoint, error) {
	st := c.reader()
	if st == nil {
		return nil, errUsageStore
	}
	buckets, err := st.Buckets(ctx, w.from, w.hours, keys)
	if err != nil {
		return nil, err
	}
	loc := c.loc()
	if w.hours {
		by := make(map[int64]analytics.Bucket, len(buckets))
		for _, b := range buckets {
			by[b.Start.UnixMilli()] = b
		}
		begin := w.from.UTC().Truncate(time.Hour)
		out := make([]usageChartPoint, 0, 24)
		for i := range 24 {
			t := begin.Add(time.Duration(i) * time.Hour)
			b := by[t.UnixMilli()]
			out = append(out, usageChartPoint{Label: t.In(loc).Format("15:04"), Tokens: b.Tokens, Cost: b.Cost, Requests: b.Requests})
		}
		return out, nil
	}

	// A day taken from the daily rollup keeps its UTC date; an hour is folded
	// into the local date it fell on.
	days := map[string]*usageChartPoint{}
	for _, b := range buckets {
		key := b.Start.UTC().Format(usageDayLayout)
		if !b.Daily {
			key = b.Start.In(loc).Format(usageDayLayout)
		}
		p := days[key]
		if p == nil {
			p = &usageChartPoint{Label: usageDayLabel(key, loc)}
			days[key] = p
		}
		p.Tokens += b.Tokens
		p.Cost += b.Cost
		p.Requests += b.Requests
	}

	var labels []string
	if n, ok := usageDays[w.key]; ok {
		mid := c.midnight()
		for i := n - 1; i >= 0; i-- {
			labels = append(labels, mid.AddDate(0, 0, -i).Format(usageDayLayout))
		}
	} else {
		if len(days) == 0 {
			return []usageChartPoint{}, nil
		}
		first := ""
		for k := range days {
			if first == "" || k < first {
				first = k
			}
		}
		d, err := time.ParseInLocation(usageDayLayout, first, loc)
		if err != nil {
			return nil, err
		}
		today := c.midnight()
		for !d.After(today) {
			labels = append(labels, d.Format(usageDayLayout))
			d = d.AddDate(0, 0, 1)
		}
	}
	out := make([]usageChartPoint, 0, len(labels))
	for _, k := range labels {
		if p := days[k]; p != nil {
			out = append(out, *p)
		} else {
			out = append(out, usageChartPoint{Label: usageDayLabel(k, loc)})
		}
	}
	return out, nil
}

// live field holders

func (t *usageTotals) add(o usageTotals) {
	t.Requests += o.Requests
	t.PromptTokens += o.PromptTokens
	t.CompletionTokens += o.CompletionTokens
	t.CachedTokens += o.CachedTokens
	t.Cost += o.Cost
}

// usageKeyName is what the usage answers may say about a stored key.
type usageKeyName struct{ name, masked string }

// keyNames reads the stored keys once, so an answer can name and mask a key
// without ever holding the key itself.
func (c *UsageController) keyNames(ctx context.Context) map[string]usageKeyName {
	out := map[string]usageKeyName{}
	st := c.store()
	if st == nil {
		return out
	}
	keys, err := st.APIKeys().List(ctx)
	if err != nil {
		return out
	}
	for _, k := range keys {
		masked := auth.MaskKey(k.Key)
		name := k.Name
		if name == "" {
			name = masked
		}
		out[k.ID] = usageKeyName{name: name, masked: masked}
	}
	return out
}

func (c *UsageController) store() *repository.Store { return storeOf(c.DB) }

// accountNames maps a connection to the name the pages show.
func (c *UsageController) accountNames(ctx context.Context) map[string]string {
	out := map[string]string{}
	st := c.store()
	if st == nil {
		return out
	}
	rows, err := st.Connections().List(ctx, repository.ConnFilter{})
	if err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ID] = connectionAccount(row)
	}
	return out
}

func connectionAccount(row repository.Connection) string {
	if name := strings.TrimSpace(row.Name); name != "" {
		return name
	}
	if email := strings.TrimSpace(row.Email); email != "" {
		return email
	}
	if row.ID == "" {
		return ""
	}
	if len(row.ID) <= 8 {
		return "Account " + row.ID
	}
	return "Account " + text.CutBytes(row.ID, 8) + "..."
}

func (c *UsageController) providerName(raw string, nodes map[string]string) string {
	if name, ok := nodes[raw]; ok {
		return name
	}
	if c.Registry != nil {
		if id, ok := c.Registry.Resolve(raw); ok {
			if e, ok := c.Registry.ByID(id); ok && strings.TrimSpace(e.Display.Name) != "" {
				return strings.TrimSpace(e.Display.Name)
			}
		}
	}
	return raw
}

// usageStatus is the recent list's word for a finished request.
func usageStatus(r analytics.Request) string {
	if r.Status < 400 {
		return "ok"
	}
	if r.ErrorClass != "" && r.ErrorClass != "none" {
		return r.ErrorClass
	}
	return strconv.FormatInt(r.Status, 10)
}

func usageLogLine(r analytics.Request, account string, loc *time.Location) string {
	status := "ok"
	if r.Status >= 400 {
		status = strconv.FormatInt(r.Status, 10)
	}
	line := fmt.Sprintf("%s | %s | %s | %s | %d | %d | %s",
		r.Time.In(loc).Format("02-01-2006 15:04:05"),
		usageField(r.Model), usageField(strings.ToUpper(r.Provider)), usageField(account),
		r.Prompt, r.Completion, status)
	// Two trailing fields, each present only when something acted, in the
	// reference's order: the skills, then the token savers.
	if r.Skills != "" {
		line += " | " + usageField(r.Skills)
	}
	if r.Savers != "" {
		line += " | " + usageField(r.Savers)
	}
	return line
}

func usageField(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}

func usageDayLabel(key string, loc *time.Location) string {
	if d, err := time.ParseInLocation(usageDayLayout, key, loc); err == nil {
		return d.Format("Jan 2")
	}
	return key
}

// providerOr names a provider a row left empty, so a map key stays readable.
func providerOr(p string) string {
	if p == "" {
		return "unknown"
	}
	return p
}
