package controllers

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/app/gateway/health"
	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// usageFake is the analytics store as the controller reads it.
type usageFake struct {
	sums        []analytics.Sum
	buckets     []analytics.Bucket
	seen        map[analytics.Dims]time.Time
	recent      []analytics.Request
	providers   []string
	err         error
	lastFrom    time.Time
	lastHours   bool
	lastKeys    []string
	unknownCost int64
	// lastRecent is the start the logs reader asked for, and lastServed the key
	// list the recent list asked for.
	lastRecent time.Time
	lastServed []string
}

func (f *usageFake) Sums(_ context.Context, from time.Time, hourly bool, keys []string) ([]analytics.Sum, error) {
	f.lastFrom, f.lastHours, f.lastKeys = from, hourly, keys
	return f.sums, f.err
}

func (f *usageFake) Buckets(_ context.Context, from time.Time, hourly bool, _ []string) ([]analytics.Bucket, error) {
	f.lastFrom, f.lastHours = from, hourly
	return f.buckets, f.err
}

func (f *usageFake) LastSeen(context.Context, time.Time, []string) (map[analytics.Dims]time.Time, error) {
	return f.seen, f.err
}

func (f *usageFake) Recent(_ context.Context, from time.Time, _ int, _ []string) ([]analytics.Request, error) {
	f.lastRecent = from
	return f.recent, f.err
}

func (f *usageFake) RecentBefore(_ context.Context, from time.Time, _ int, _ []string, cursor analytics.Cursor) ([]analytics.Request, error) {
	f.lastRecent = from
	var out []analytics.Request
	for _, r := range f.recent {
		if r.Time.UnixMilli() < cursor.TS || (r.Time.UnixMilli() == cursor.TS && r.ID < cursor.ID) {
			out = append(out, r)
		}
	}
	return out, f.err
}

// RecentServed applies the store's rule: only rows that used tokens, at most
// limit of them.
func (f *usageFake) RecentServed(_ context.Context, limit int, keys []string) ([]analytics.Request, error) {
	f.lastServed = keys
	var out []analytics.Request
	for _, r := range f.recent {
		if r.Prompt+r.Completion > 0 && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, f.err
}

func (f *usageFake) UnknownCosts(context.Context, time.Time, []string) (int64, error) {
	return f.unknownCost, nil
}

func (f *usageFake) Providers(context.Context, []string) ([]string, error) {
	return f.providers, f.err
}

type usageRig struct {
	m       *mgmtRig
	c       *UsageController
	f       *usageFake
	flights *health.Inflight
	now     time.Time
	loc     *time.Location
	flushed atomic.Uint64
	conn    repository.Connection
	key     repository.APIKey
	plain   string
}

var usageNow = time.Date(2026, 11, 17, 10, 30, 0, 0, time.UTC)

func newUsageRig(t *testing.T) *usageRig {
	t.Helper()
	m := newMgmtRig(t)
	ctx := context.Background()
	conn, err := m.st.Store.Connections().Create(ctx, repository.ConnInput{
		Provider: "openai", AuthType: "apikey", Name: "work",
		Fields: map[string]string{"apiKey": `"sk-test"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	plain := "sk-live-abcdef1234567890"
	key, err := m.st.Store.APIKeys().Create(ctx, "Work Key", "", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.st.Store.Nodes().CreateUnique(ctx, repository.Node{
		ID: "cc", Type: upstream.NodeTypeAnthropic, Name: "Claude Code", Data: `{"baseUrl":"https://cc.example","apiType":"claude"}`,
	}, ""); err != nil {
		t.Fatal(err)
	}

	r := &usageRig{m: m, now: usageNow, loc: time.FixedZone("plus7", 7*3600), conn: conn, key: key, plain: plain,
		flights: health.NewInflight(nil)}
	r.f = &usageFake{}
	r.c = &UsageController{
		Analytics: func() UsageReader { return r.f },
		Flushed:   func() uint64 { return r.flushed.Load() },
		DB:        func() *repository.Store { return m.st.Store },
		Registry:  m.pc.Registry, Inflight: r.flights,
		Loc: r.loc, Now: func() time.Time { return r.now },
	}
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/stats", r.c.Stats)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/chart", r.c.Chart)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/history", r.c.History)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/logs", r.c.Logs)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/providers", r.c.Providers)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/active-providers", r.c.ActiveProviders)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/stream", r.c.Stream)
	return r
}

// usageSums seeds the two rows every test reads.
func (r *usageRig) sums() (a, b analytics.Sum) {
	a = analytics.Sum{
		Dims: analytics.Dims{
			Provider: "openai", Model: "gpt-4o", ConnectionID: r.conn.ID, APIKeyID: r.key.ID,
			Endpoint: "/v1/chat/completions",
		},
		Requests: 3, Errors: 1, Prompt: 100, Completion: 50, Cached: 10, Cost: 0.25,
		Last: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
	}
	b = analytics.Sum{
		Dims:     analytics.Dims{Provider: "cc", Model: "claude-x", Endpoint: "/v1/messages"},
		Requests: 1, Prompt: 5, Completion: 5, Cost: 0.5,
		Last: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC),
	}
	return a, b
}

func TestUsageStatsNameKeysWithoutEverHoldingOne(t *testing.T) {
	r := newUsageRig(t)
	a, b := r.sums()
	r.f.sums = []analytics.Sum{a, b}
	r.f.seen = map[analytics.Dims]time.Time{a.Dims: time.Date(2026, 11, 17, 9, 45, 0, 0, time.UTC)}

	code, _, raw := r.m.call("GET", "/api/usage/stats?period=all", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, r.plain) {
		t.Fatalf("the answer carries the key: %s", raw)
	}
	for name, want := range map[string]float64{
		"totalRequests": 4, "totalPromptTokens": 105, "totalCompletionTokens": 55,
		"totalCachedTokens": 10, "totalCost": 0.75,
	} {
		if got := gjson.Get(raw, name).Float(); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	display := "OpenAI"
	if e, ok := r.m.pc.Registry.ByID("openai"); ok && e.Display.Name != "" {
		display = e.Display.Name
	}

	model := gjson.Get(raw, `byModel.gpt-4o\ \(openai\)`)
	if node := gjson.Get(raw, "byProvider.cc.name").String(); node != "Claude Code" {
		t.Errorf("the chart names a node id %q, want its node name", node)
	}
	if reg := gjson.Get(raw, "byProvider.openai.name").String(); reg != display {
		t.Errorf("byProvider.openai.name = %q, want %q", reg, display)
	}
	if model.Get("requests").Int() != 3 || model.Get("provider").String() != display || model.Get("rawModel").String() != "gpt-4o" {
		t.Fatalf("byModel %s", model.Raw)
	}
	if got := model.Get("lastUsed").String(); got != "2026-11-17T09:45:00.000Z" {
		t.Fatalf("the exact time was not used: %q", got)
	}
	account := gjson.Get(raw, `byAccount.gpt-4o\ \(`+display+`\ -\ work\)`)
	if account.Get("connectionId").String() != r.conn.ID || account.Get("accountName").String() != "work" {
		t.Fatalf("byAccount %s", account.Raw)
	}
	keyed := gjson.Get(raw, `byApiKey.`+r.key.ID+`\|gpt-4o\|openai`)
	if keyed.Get("keyName").String() != "Work Key" || keyed.Get("apiKeyMasked").String() != auth.MaskKey(r.plain) {
		t.Fatalf("byApiKey %s", keyed.Raw)
	}
	local := gjson.Get(raw, `byApiKey.local-no-key\|claude-x\|cc`)
	if local.Get("keyName").String() != "Local (No API Key)" || local.Get("apiKeyKey").String() != "local-no-key" {
		t.Fatalf("a request with no key %s", local.Raw)
	}
	endpoint := gjson.Get(raw, `byEndpoint./v1/chat/completions\|gpt-4o\|openai`)
	if endpoint.Get("requests").Int() != 3 || endpoint.Get("rawModel").String() != "gpt-4o" ||
		endpoint.Get("provider").String() != display || endpoint.Get("lastUsed").String() != "2026-11-17T09:45:00.000Z" {
		t.Fatalf("byEndpoint %s", endpoint.Raw)
	}
	if gjson.Get(raw, `byEndpoint./v1/messages\|claude-x\|cc`).Get("requests").Int() != 1 {
		t.Fatalf("byEndpoint %s", raw)
	}
	if gjson.Get(raw, "byProvider.cc.provider").Exists() {
		t.Fatal("a provider entry carries a field it has no use for")
	}
}

func TestAKeyThatIsNoLongerStoredIsNamedByAShortIdAndNeverInFull(t *testing.T) {
	r := newUsageRig(t)
	a, _ := r.sums()
	a.APIKeyID = "0123456789abcdef-deleted-key"
	r.f.sums = []analytics.Sum{a}
	code, _, raw := r.m.call("GET", "/api/usage/stats?period=all", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	entry := gjson.Get(raw, `byApiKey.0123456789abcdef-deleted-key\|gpt-4o\|openai`)
	if entry.Get("keyName").String() != "01234567" || entry.Get("apiKeyMasked").String() != "01234567" {
		t.Fatalf("a deleted key's entry: %s", entry.Raw)
	}
}

// The health probe is a caller, not a stored key: its spend is read under its
// own name rather than the first eight bytes of its id.
func TestTheProbeKeyIsNamedInFull(t *testing.T) {
	r := newUsageRig(t)
	a, _ := r.sums()
	a.APIKeyID = identity.Probe().ID
	r.f.sums = []analytics.Sum{a}
	code, _, raw := r.m.call("GET", "/api/usage/stats?period=all", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	entry := gjson.Get(raw, `byApiKey.`+identity.Probe().ID+`\|gpt-4o\|openai`)
	if entry.Get("keyName").String() != "System Probe" || entry.Get("apiKeyMasked").String() != identity.Probe().ID {
		t.Fatalf("the probe's entry: %s", entry.Raw)
	}
}

func TestUsageStatsCarryTheLiveRequests(t *testing.T) {
	r := newUsageRig(t)
	release := r.flights.Start("openai", "gpt-4o", r.conn.ID)
	other := r.flights.Start("openai", "gpt-4o", r.conn.ID)
	defer other(false)
	r.flights.Start("cc", "claude-x", "gone")(true)
	cc := r.flights.Start("cc", "claude-x", "gone")
	defer cc(false)

	code, _, raw := r.m.call("GET", "/api/usage/stats?period=today", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := gjson.Get(raw, `pending.byModel.gpt-4o\ \(openai\)`).Int(); got != 2 {
		t.Fatalf("pending %s", raw)
	}
	if got := gjson.Get(raw, "pending.byAccount."+r.conn.ID+`.gpt-4o\ \(openai\)`).Int(); got != 2 {
		t.Fatalf("pending by account %s", raw)
	}
	if got := gjson.Get(raw, "activeRequests.#(provider==\"cc\").account").String(); got != "Account gone" {
		t.Fatalf("active %s", raw)
	}
	if got := gjson.Get(raw, "errorProvider").String(); got != "cc" {
		t.Fatalf("errorProvider %q", got)
	}
	release(false)
	if !strings.Contains(raw, `"recentRequests":[]`) {
		t.Fatalf("an empty recent list: %s", raw)
	}
}

func TestUsageChartsLayOutHoursAndLocalDays(t *testing.T) {
	r := newUsageRig(t)
	code, _, raw := r.m.call("GET", "/api/usage/chart?period=today", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	points := gjson.Parse(raw).Array()
	if len(points) != 24 || points[0].Get("label").String() != "00:00" || points[23].Get("label").String() != "23:00" {
		t.Fatalf("today %s", raw)
	}

	code, _, raw = r.m.call("GET", "/api/usage/chart?period=24h", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if got := gjson.Parse(raw).Array()[0].Get("label").String(); got != "18:00" {
		t.Fatalf("24h starts at %q", got)
	}

	r.f.buckets = []analytics.Bucket{
		{Start: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Daily: true, Tokens: 3, Requests: 1},
		{Start: time.Date(2026, 11, 16, 17, 30, 0, 0, time.UTC), Tokens: 7, Requests: 2},
	}
	code, _, raw = r.m.call("GET", "/api/usage/chart?period=7d", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	points = gjson.Parse(raw).Array()
	if len(points) != 7 || points[0].Get("label").String() != "Nov 11" || points[6].Get("label").String() != "Nov 17" {
		t.Fatalf("7d %s", raw)
	}
	if points[3].Get("tokens").Int() != 3 || points[6].Get("tokens").Int() != 7 {
		t.Fatalf("the hourly row was not folded into its local day: %s", raw)
	}

	r.f.buckets = nil
	code, _, raw = r.m.call("GET", "/api/usage/chart?period=all", "", true)
	if code != 200 || strings.TrimSpace(raw) != "[]" {
		t.Fatalf("an empty all chart: %d %q", code, raw)
	}
	r.f.buckets = []analytics.Bucket{
		{Start: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Daily: true, Tokens: 3},
		{Start: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), Tokens: 7},
	}
	code, _, raw = r.m.call("GET", "/api/usage/chart?period=all", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	points = gjson.Parse(raw).Array()
	if len(points) != 4 || points[0].Get("label").String() != "Nov 14" || points[3].Get("tokens").Int() != 7 {
		t.Fatalf("all %s", raw)
	}
}

func TestAnUnknownPeriodIsRefusedOnEveryRoute(t *testing.T) {
	r := newUsageRig(t)
	for _, path := range []string{"/api/usage/stats?period=nope", "/api/usage/chart?period=nope", "/api/usage/active-providers?period=nope", "/api/usage/logs?period=nope"} {
		code, out, raw := r.m.call("GET", path, "", true)
		if code != 400 || out["error"] != "Invalid period" {
			t.Fatalf("%s: %d %s", path, code, raw)
		}
	}
}

func TestUsageLogsAreOneLinePerRow(t *testing.T) {
	r := newUsageRig(t)
	r.f.recent = []analytics.Request{
		{ID: 7, Time: time.Date(2026, 11, 17, 9, 5, 6, 0, time.UTC), Provider: "openai", Model: "gpt-4o", ConnectionID: r.conn.ID, APIKeyName: "ci", Prompt: 12, Completion: 7, Status: 200},
		{ID: 6, Time: time.Date(2026, 11, 17, 9, 6, 0, 0, time.UTC), Provider: "cc", ConnectionID: "missing", Prompt: 1, Status: 502, ErrorClass: "upstream_5xx"},
	}
	code, _, raw := r.m.call("GET", "/api/usage/logs", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	lines := gjson.Parse(raw).Array()
	if len(lines) != 2 {
		t.Fatalf("lines %s", raw)
	}
	if got := lines[0].String(); got != "17-11-2026 16:05:06 | gpt-4o | OPENAI | work | ci | 12 | 7 | ok" {
		t.Fatalf("first line %q", got)
	}
	if got := lines[1].String(); got != "17-11-2026 16:06:00 | - | Claude Code | - | - | 1 | 0 | 502" {
		t.Fatalf("second line %q", got)
	}
	// A page after a cursor reads only what is older than that position.
	older := time.Date(2026, 11, 17, 9, 6, 0, 0, time.UTC).UnixMilli()
	code, _, raw = r.m.call("GET", "/api/usage/logs?before="+strconv.FormatInt(older, 10)+"-6", "", true)
	if code != 200 || len(gjson.Parse(raw).Array()) != 1 {
		t.Fatalf("after cursor: %d %s", code, raw)
	}
	if code, _, _ := r.m.call("GET", "/api/usage/logs?before=nope", "", true); code != 400 {
		t.Fatalf("a bad cursor answered %d", code)
	}
}

// TestTheLogsStartSevenDaysBack covers the default the page relies on: the
// reader is handed a start, not every row the store holds.
func TestTheLogsStartSevenDaysBack(t *testing.T) {
	r := newUsageRig(t)
	if code, _, raw := r.m.call("GET", "/api/usage/logs", "", true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	want := time.Date(usageNow.Year(), usageNow.Month(), usageNow.Day()-6, 0, 0, 0, 0, r.loc)
	if !r.f.lastRecent.Equal(want) {
		t.Fatalf("the logs start %v, want %v", r.f.lastRecent, want)
	}
	if code, _, raw := r.m.call("GET", "/api/usage/logs?period=all", "", true); code != 200 || !r.f.lastRecent.IsZero() {
		t.Fatalf("%d %v %s", code, r.f.lastRecent, raw)
	}
}

func TestUsageProvidersAndActiveProviders(t *testing.T) {
	r := newUsageRig(t)
	r.f.providers = []string{"cc", "openai"}
	code, out, raw := r.m.call("GET", "/api/usage/providers", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	list := out["providers"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["name"] != "Claude Code" {
		t.Fatalf("providers %v", list)
	}
	if name := list[1].(map[string]any)["name"].(string); name == "" || name == "openai" {
		t.Fatalf("the registry name is missing: %v", list[1])
	}

	keep := []func(bool){r.flights.Start("openai", "gpt-4o", r.conn.ID),
		r.flights.Start("openai", "gpt-4o", r.conn.ID), r.flights.Start("cc", "claude-x", "c9")}
	defer func() {
		for _, release := range keep {
			release(false)
		}
	}()
	code, out, raw = r.m.call("GET", "/api/usage/active-providers?period=7d", "", true)
	if code != 200 || out["activeProviders"] != float64(2) || out["period"] != "7d" {
		t.Fatalf("%d %s", code, raw)
	}
}

func TestUsageHistoryAsksForEverything(t *testing.T) {
	r := newUsageRig(t)
	if code, _, raw := r.m.call("GET", "/api/usage/history", "", true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if !r.f.lastFrom.IsZero() || r.f.lastHours {
		t.Fatalf("history read %v hourly=%v", r.f.lastFrom, r.f.lastHours)
	}
	if code, _, raw := r.m.call("GET", "/api/usage/stats", "", true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	want := time.Date(2026, 11, 11, 0, 0, 0, 0, r.loc)
	if !r.f.lastFrom.Equal(want) || r.f.lastHours {
		t.Fatalf("the default period starts at %v, want %v", r.f.lastFrom, want)
	}
	if code, _, raw := r.m.call("GET", "/api/usage/stats?period=today", "", true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if want := time.Date(2026, 11, 17, 0, 0, 0, 0, r.loc); !r.f.lastFrom.Equal(want) || !r.f.lastHours {
		t.Fatalf("today starts at %v, want %v", r.f.lastFrom, want)
	}
}

func TestTheUsageStreamSendsTheLiveListOnChange(t *testing.T) {
	r := newUsageRig(t)
	r.c.every = 10 * time.Millisecond
	r.c.beat = 50 * time.Millisecond

	srv := httptest.NewServer(r.m.route)
	defer srv.Close()
	req, err := nethttp.NewRequest(nethttp.MethodGet, srv.URL+"/api/usage/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: r.m.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	if err != nil || !strings.Contains(first, `"activeRequests":[]`) || !strings.Contains(first, `"pending"`) {
		t.Fatalf("first frame %q (%v)", first, err)
	}

	release := r.flights.Start("openai", "gpt-4o", r.conn.ID)
	defer release(false)
	seen := false
	for i := 0; i < 60 && !seen; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended early: %v", err)
		}
		if strings.Contains(line, `"count":1`) && strings.Contains(line, `"account":"work"`) {
			seen = true
		}
	}
	if !seen {
		t.Fatal("the live request never arrived")
	}
	r.flushed.Add(1)
	sent := false
	for i := 0; i < 60 && !sent; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended early: %v", err)
		}
		sent = strings.HasPrefix(line, "data:")
	}
	if !sent {
		t.Fatal("a batch did not trigger a frame")
	}
}

func TestACanceledReadIsNotLoggedAsAFailureButARealOneIs(t *testing.T) {
	r := newUsageRig(t)
	logs := &tests.Log{}
	r.c.Log = logs

	r.f.err = fmt.Errorf("query: %w", context.Canceled)
	r.m.call("GET", "/api/usage/logs", "", true)
	if lines := logs.Lines(); len(lines) != 0 {
		t.Fatalf("a request the client abandoned was logged: %v", lines)
	}

	r.f.err = errors.New("disk is full")
	r.m.call("GET", "/api/usage/logs", "", true)
	if lines := logs.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "disk is full") {
		t.Fatalf("a real failure was not logged once: %v", lines)
	}
}
