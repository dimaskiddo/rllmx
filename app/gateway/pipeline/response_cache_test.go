package pipeline

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/account"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/platform/cache"
	"rllmx/tests"
)

const (
	cacheOn  = `{"responseCache":{"enabled":true,"ttlSeconds":600,"maxEntryKb":512}}`
	toolJSON = `{"id":"chatcmpl-abc12345","object":"chat.completion","created":1,"model":"gpt-x","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
)

type flags struct {
	on  map[string]bool
	err error
}

func (f *flags) ResponseCacheEnabled(_ context.Context, id string) (bool, error) {
	return f.on[id], f.err
}

// failingSelector fails the test's request if anything asks it for an account.
type failingSelector struct{}

func (failingSelector) Select(context.Context, account.Request) (account.Selection, error) {
	return account.Selection{}, errors.New("a cache hit asked for an account")
}

type cacheRig struct {
	*rig
	flags *flags
	store *tests.Cache
	c     *cache.Cache
	bud   *cache.Budget
}

func newCacheRig(t *testing.T, steps ...tests.Reply) *cacheRig {
	t.Helper()
	r := newRig(t, steps...)
	r.conn(nodeID, "k1")
	facade := tests.NewCache()
	c := cache.New(facade, cache.WithoutShared())
	cr := &cacheRig{rig: r, flags: &flags{on: map[string]bool{"key-1": true}}, store: facade, c: c, bud: &cache.Budget{MaxEntries: 100, MaxBytes: 1 << 20}}
	r.p.d.Settings = tests.Settings{JSON: cacheOn}
	r.p.d.ResponseCache = &ResponseCache{Cache: c, Flags: cr.flags, Budget: cr.bud}
	return cr
}

func (r *cacheRig) send(in Input) (Output, struct{}) {
	r.t.Helper()
	if in.Path == "" {
		in.Path = "/v1/chat/completions"
	}
	if in.Caller.ID == "" && !in.Caller.Anonymous {
		in.Caller = identity.Caller{ID: "key-1", Name: "ci"}
	}
	before := len(r.rows.All())
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	out := r.p.Handle(context.Background(), in, open)
	rows := r.rows.All()
	if len(rows) != before+1 {
		r.t.Fatalf("a request left %d rows, want 1", len(rows)-before)
	}
	return out, struct{}{}
}

func (r *cacheRig) use(body string) Input {
	return Input{Body: []byte(body), Cache: "use"}
}

func TestACachedAnswerIsReplayedWithNoUpstreamAndNoAccount(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	first, _ := r.send(r.use(chatBody))
	if first.Status != 200 || first.Header.Get(CacheHeader) != "miss" {
		t.Fatalf("%d %v", first.Status, first.Header)
	}
	succeeded := r.rep.succeeded.Load()

	second, _ := r.send(r.use(chatBody))
	if second.Status != 200 || second.Header.Get(CacheHeader) != "hit" || string(second.Body) != string(first.Body) {
		t.Fatalf("%d %v\n%s\n%s", second.Status, second.Header, first.Body, second.Body)
	}
	if second.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%v", second.Header)
	}
	if n := len(r.up.Calls()); n != 1 {
		t.Fatalf("the upstream was called %d times", n)
	}
	if r.rep.succeeded.Load() != succeeded {
		t.Fatal("a hit cleared a lock")
	}
	row := r.last()
	if !row.Cached || row.Status != 200 || row.Cost != 0 || row.ConnectionID != "" || row.Attempt != 0 ||
		row.PromptTokens != 10 || row.CompletionTokens != 5 || row.CacheWriteTokens != 0 ||
		row.Provider != nodeID || row.Model != "gpt-x" || row.APIKeyID != "key-1" || row.Stream {
		t.Fatalf("%+v", row)
	}
	if r.rows.All()[0].Cached {
		t.Fatal("the first, real call was marked cached")
	}
}

func TestAHitNeverSelectsAnAccount(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.send(r.use(chatBody))
	r.p.d.Selector = failingSelector{}
	out, _ := r.send(r.use(chatBody))
	if out.Status != 200 || out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("%d %v %s", out.Status, out.Header, out.Body)
	}
}

func TestOnlyAnExplicitZeroTemperatureCountsAsAWish(t *testing.T) {
	cases := map[string]bool{
		`{"model":"m","temperature":0,"messages":[{"role":"user","content":"hi"}]}`:    true,
		`{"model":"m","temperature":0.0,"messages":[{"role":"user","content":"hi"}]}`:  true,
		`{"model":"m","temperature":0.2,"messages":[{"role":"user","content":"hi"}]}`:  false,
		`{"model":"m","temperature":null,"messages":[{"role":"user","content":"hi"}]}`: false,
		`{"model":"m","temperature":"0","messages":[{"role":"user","content":"hi"}]}`:  false,
		chatBody: false,
	}
	for body, want := range cases {
		r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		out, _ := r.send(Input{Body: []byte(body)})
		if got := out.Header.Get(CacheHeader) == "miss"; got != want {
			t.Errorf("%s: consulted = %v, want %v", body, got, want)
		}
	}
}

func TestTheCacheIsOffUnlessEverySwitchIsOn(t *testing.T) {
	cases := map[string]func(r *cacheRig, in *Input){
		"setting off": func(r *cacheRig, _ *Input) {
			r.p.d.Settings = tests.Settings{JSON: `{"responseCache":{"enabled":false}}`}
		},
		"setting absent":   func(r *cacheRig, _ *Input) { r.p.d.Settings = tests.Settings{} },
		"setting a scalar": func(r *cacheRig, _ *Input) { r.p.d.Settings = tests.Settings{JSON: `{"responseCache":true}`} },
		"no settings":      func(r *cacheRig, _ *Input) { r.p.d.Settings = nil },
		"key flag off":     func(r *cacheRig, _ *Input) { r.flags.on["key-1"] = false },
		"flag read fails":  func(r *cacheRig, _ *Input) { r.flags.err = errors.New("db down") },
		"anonymous":        func(_ *cacheRig, in *Input) { in.Caller = identity.AnonymousLocal() },
		"bypass":           func(_ *cacheRig, in *Input) { in.Cache = "bypass" },
		"stream":           func(_ *cacheRig, in *Input) { in.Body = []byte(chatStream) },
		"no wish":          func(_ *cacheRig, in *Input) { in.Cache = "" },
		"duplicate key": func(_ *cacheRig, in *Input) {
			in.Body = []byte(`{"model":"m","model":"m","messages":[{"role":"user","content":"hi"}]}`)
		},
		"no cache wired": func(r *cacheRig, _ *Input) { r.p.d.ResponseCache = nil },
	}
	for name, mutate := range cases {
		r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON}, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
		in := r.use(chatBody)
		mutate(r, &in)
		for i := 0; i < 2; i++ {
			out, _ := r.send(in)
			if h := out.Header.Get(CacheHeader); h != "" {
				t.Errorf("%s: call %d carried %s", name, i+1, h)
			}
		}
		if n := len(r.up.Calls()); n != 2 {
			t.Errorf("%s: %d upstream calls, want 2", name, n)
		}
	}
}

func TestAnAnonymousCallerIsNeverCachedEvenWithTheFlagOn(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	anon := identity.AnonymousLocal()
	r.flags.on[anon.ID] = true
	in := r.use(chatBody)
	in.Caller = anon
	for i := 0; i < 2; i++ {
		if out, _ := r.send(in); out.Header.Get(CacheHeader) != "" {
			t.Fatalf("call %d: %v", i+1, out.Header)
		}
	}
	if len(r.up.Calls()) != 2 {
		t.Fatal("an anonymous answer was cached")
	}
}

func TestBypassNeitherReadsNorWrites(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	bypass := r.use(chatBody)
	bypass.Cache = "bypass"
	r.send(bypass)
	if hit, _ := r.send(r.use(chatBody)); hit.Header.Get(CacheHeader) != "miss" {
		t.Fatalf("bypass wrote an entry: %v", hit.Header)
	}
	if hit, _ := r.send(r.use(chatBody)); hit.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("setup: the entry should exist now: %v", hit.Header)
	}
	calls := len(r.up.Calls())
	if out, _ := r.send(bypass); out.Header.Get(CacheHeader) != "" || len(r.up.Calls()) != calls+1 {
		t.Fatalf("bypass read an entry: %v, %d calls", out.Header, len(r.up.Calls())-calls)
	}
}

func TestWhatChangesTheAnswerChangesTheKey(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.flags.on["key-2"] = true
	r.send(r.use(chatBody))
	variants := map[string]Input{
		"another key":   {Body: []byte(chatBody), Cache: "use", Caller: identity.Caller{ID: "key-2", Name: "ci2"}},
		"another body":  r.use(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`),
		"another model": r.use(`{"model":"` + nodeID + `/other","messages":[{"role":"user","content":"hi"}]}`),
		"context mark":  r.use(`{"model":"m[1m]","messages":[{"role":"user","content":"hi"}]}`),
		"thinking":      r.use(`{"model":"m(high)","messages":[{"role":"user","content":"hi"}]}`),
		"claude route":  {Path: "/v1/messages", Body: []byte(`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`), Cache: "use"},
	}
	for name, in := range variants {
		out, _ := r.send(in)
		if out.Header.Get(CacheHeader) == "hit" {
			t.Errorf("%s: shared an entry", name)
		}
	}
	if out, _ := r.send(r.use(`{"model":"m(high)","messages":[{"role":"user","content":"hi"}]}`)); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("the same thinking request must hit once stored: %v", out.Header)
	}
	same := r.use(`{"messages":[{"content":"hi","role":"user"}],"stream":false,"model":"m"}`)
	if out, _ := r.send(same); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("key order and stream must not matter: %v", out.Header)
	}
}

func TestOnlyACleanTwoHundredIsStored(t *testing.T) {
	t.Run("tool calls", func(t *testing.T) {
		r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: toolJSON})
		for i := 0; i < 2; i++ {
			if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "miss" {
				t.Fatalf("call %d: %v", i+1, out.Header)
			}
		}
		if len(r.up.Calls()) != 2 {
			t.Fatal("a tool-call answer was served from the cache")
		}
	})
	t.Run("errors", func(t *testing.T) {
		r := newCacheRig(t, tests.Reply{Status: 400, ContentType: "application/json", Body: `{"error":{"message":"bad"}}`})
		for i := 0; i < 2; i++ {
			out, _ := r.send(r.use(chatBody))
			if out.Status != 400 || out.Header.Get(CacheHeader) != "" {
				t.Fatalf("call %d: %d %v", i+1, out.Status, out.Header)
			}
		}
		if len(r.up.Calls()) != 2 {
			t.Fatal("an error was served from the cache")
		}
	})
	t.Run("oversize", func(t *testing.T) {
		big := strings.Replace(okJSON, `"hello"`, `"`+strings.Repeat("x", 2000)+`"`, 1)
		r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: big})
		r.p.d.Settings = tests.Settings{JSON: `{"responseCache":{"enabled":true,"maxEntryKb":1}}`}
		for i := 0; i < 2; i++ {
			if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "miss" {
				t.Fatalf("call %d: %v", i+1, out.Header)
			}
		}
		if len(r.up.Calls()) != 2 {
			t.Fatal("an answer over maxEntryKb was stored")
		}
	})
	t.Run("full budget", func(t *testing.T) {
		r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		r.p.d.ResponseCache.Budget = &cache.Budget{MaxEntries: 1, MaxBytes: 1 << 20}
		r.send(r.use(chatBody))
		other := r.use(`{"model":"m","messages":[{"role":"user","content":"second"}]}`)
		r.send(other)
		if out, _ := r.send(other); out.Header.Get(CacheHeader) != "miss" {
			t.Fatalf("an answer past the entry limit was stored: %v", out.Header)
		}
		if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "hit" {
			t.Fatalf("the first entry was lost: %v", out.Header)
		}
	})
}

func TestAForcedStreamProviderIsStillCachedForANonStreamClient(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "fk")
	in := r.use(`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	first, _ := r.send(in)
	if first.Status != 200 || first.Header.Get(CacheHeader) != "miss" || gjson.GetBytes(first.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %v %s", first.Status, first.Header, first.Body)
	}
	if second, _ := r.send(in); second.Header.Get(CacheHeader) != "hit" || len(r.up.Calls()) != 1 {
		t.Fatalf("%v %d", second.Header, len(r.up.Calls()))
	}
}

func TestEveryClientFormatCanHit(t *testing.T) {
	bodies := map[string]string{
		"/v1/messages":                     `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":                    `{"model":"m","input":"hi"}`,
		"/v1/api/chat":                     `{"model":"m","stream":false,"messages":[{"role":"user","content":"hi"}]}`,
		"/v1beta/models/m:generateContent": `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"temperature":0}}`,
	}
	for path, body := range bodies {
		r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		in := Input{Path: path, Body: []byte(body)}
		if path != "/v1beta/models/m:generateContent" {
			in.Cache = "use"
		}
		first, _ := r.send(in)
		second, _ := r.send(in)
		if first.Header.Get(CacheHeader) != "miss" || second.Header.Get(CacheHeader) != "hit" || string(first.Body) != string(second.Body) || len(r.up.Calls()) != 1 {
			t.Errorf("%s: %v / %v, %d calls", path, first.Header, second.Header, len(r.up.Calls()))
		}
	}
}

func TestAnExpiredEntryAndAPurgeBothMiss(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.send(r.use(chatBody))
	r.c.Purge(cache.Response)
	if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "miss" {
		t.Fatalf("a purged entry was served: %v", out.Header)
	}
	r.p.d.Settings = tests.Settings{JSON: `{"responseCache":{"enabled":false}}`}
	if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("the projection is cached for its window, so a settings read must not run per request: %v", out.Header)
	}
	r.c.Purge(cache.Settings)
	if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "" {
		t.Fatalf("a purge of the projection must switch the cache off: %v", out.Header)
	}
}

func TestTheStoredValueHoldsNoRequestTextAndNoCredential(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.send(Input{Body: []byte(`{"model":"m","messages":[{"role":"user","content":"my secret prompt"}]}`), Cache: "use", Caller: identity.Caller{ID: "key-1", Name: "ci"}})
	keys := r.store.StoreNamed("memory").Keys("")
	found := false
	for _, k := range keys {
		if strings.Contains(k, "my secret prompt") || strings.Contains(k, "k1") {
			t.Fatalf("a cache key holds request text or a credential: %q", k)
		}
		if strings.HasPrefix(k, "response:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no response entry among %v", keys)
	}
}

func TestAKeyFlagAndASettingProjectionAreReadThroughTheCache(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	for i := 0; i < 3; i++ {
		r.send(r.use(chatBody))
	}
	if n := len(r.up.Calls()); n != 1 {
		t.Fatalf("%d upstream calls", n)
	}
	r.flags.on["key-1"] = false
	if out, _ := r.send(r.use(chatBody)); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("the flag must be cached for its window: %v", out.Header)
	}
}

func TestProjectSettingsFillsAndBoundsEveryField(t *testing.T) {
	cases := []struct {
		raw     string
		present bool
		want    cacheSettings
	}{
		{``, false, cacheSettings{TTL: time.Hour, MaxKB: 512}},
		{`{"enabled":true}`, true, cacheSettings{Enabled: true, TTL: time.Hour, MaxKB: 512}},
		{`{"enabled":true,"ttlSeconds":60,"maxEntryKb":8}`, true, cacheSettings{Enabled: true, TTL: time.Minute, MaxKB: 8}},
		{`{"enabled":true,"ttlSeconds":0,"maxEntryKb":0}`, true, cacheSettings{Enabled: true, TTL: time.Hour, MaxKB: 512}},
		{`{"enabled":true,"ttlSeconds":99999999,"maxEntryKb":99999}`, true, cacheSettings{Enabled: true, TTL: time.Hour, MaxKB: 512}},
		{`{"enabled":true,"ttlSeconds":"60"}`, true, cacheSettings{Enabled: true, TTL: time.Hour, MaxKB: 512}},
		{`{"enabled":"true"}`, true, cacheSettings{TTL: time.Hour, MaxKB: 512}},
		{`null`, true, cacheSettings{}},
		{`true`, true, cacheSettings{}},
		{`[]`, true, cacheSettings{}},
	}
	for _, c := range cases {
		if got := projectSettings(c.raw, c.present); got != c.want {
			t.Errorf("%q: %+v, want %+v", c.raw, got, c.want)
		}
	}
}

func TestBypassWinsOverAZeroTemperature(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	zero := `{"model":"m","temperature":0,"messages":[{"role":"user","content":"hi"}]}`
	r.send(Input{Body: []byte(zero)})
	if out, _ := r.send(Input{Body: []byte(zero)}); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("setup: %v", out.Header)
	}
	calls := len(r.up.Calls())
	if out, _ := r.send(Input{Body: []byte(zero), Cache: "bypass"}); out.Header.Get(CacheHeader) != "" || len(r.up.Calls()) != calls+1 {
		t.Fatalf("bypass read an entry: %v, %d calls", out.Header, len(r.up.Calls())-calls)
	}
}

func TestAContextMarkerIsAnotherRequest(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	plain := r.use(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)
	marked := r.use(`{"model":"m[1m]","messages":[{"role":"user","content":"hi"}]}`)
	r.send(plain)
	r.send(marked)
	if len(r.up.Calls()) != 2 {
		t.Fatalf("the marked request shared the plain request's entry: %d calls", len(r.up.Calls()))
	}
	if out, _ := r.send(marked); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("%v", out.Header)
	}
}

func TestAHitIsAnsweredBeforeAnythingIsFetched(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: geminiOK})
	r.conn("gem", "gk")
	body := imageBody("gem/g-vision", "https://img.example/a.png")
	fetched := &countingPrefetch{}
	r.p.d.Prefetch = fetched
	first, _ := r.send(r.use(body))
	if first.Status != 200 || fetched.n != 1 {
		t.Fatalf("setup: %d, %d fetches: %s", first.Status, fetched.n, first.Body)
	}
	if out, _ := r.send(r.use(body)); out.Header.Get(CacheHeader) != "hit" || fetched.n != 1 {
		t.Fatalf("%v, %d fetches: a hit must not fetch anything", out.Header, fetched.n)
	}
}

type countingPrefetch struct{ n int }

func (p *countingPrefetch) Inline(context.Context, *translate.Request) int {
	p.n++
	return 0
}

func TestThinkingFromTheNameKeepsItsOwnEntryOnAReasoningModel(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: geminiOK})
	r.conn("gem", "gk")
	ask := func(model string) Input {
		return r.use(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
	}
	r.send(ask("gem/gemini-2.5-pro(low)"))
	if len(r.up.Calls()) != 1 {
		t.Fatalf("setup: %d calls", len(r.up.Calls()))
	}
	r.send(ask("gem/gemini-2.5-pro(high)"))
	if len(r.up.Calls()) != 2 {
		t.Fatalf("two thinking levels shared an entry: %d calls", len(r.up.Calls()))
	}
	if out, _ := r.send(ask("gem/gemini-2.5-pro(low)")); out.Header.Get(CacheHeader) != "hit" || len(r.up.Calls()) != 2 {
		t.Fatalf("%v, %d calls", out.Header, len(r.up.Calls()))
	}
}

func TestTheKeyHoldsEveryPartThatDecidesTheAnswer(t *testing.T) {
	mk := func(edit func(c *call)) string {
		c := &call{
			in:     Input{Caller: identity.Caller{ID: "key-1"}},
			format: "openai", dest: destination{provider: "p", model: "m"},
			tctx: translate.NewCtx("openai", "openai"),
		}
		c.tctx.Thinking = translate.Thinking{Mode: "level", Level: "low", Budget: 0}
		edit(c)
		return c.requestKey([]byte(`{"a":1}`))
	}
	base := mk(func(*call) {})
	for name, edit := range map[string]func(c *call){
		"key id":          func(c *call) { c.in.Caller.ID = "key-2" },
		"client format":   func(c *call) { c.format = "claude" },
		"provider":        func(c *call) { c.dest.provider = "q" },
		"model":           func(c *call) { c.dest.model = "n" },
		"thinking mode":   func(c *call) { c.tctx.Thinking.Mode = "budget" },
		"thinking level":  func(c *call) { c.tctx.Thinking.Level = "high" },
		"thinking budget": func(c *call) { c.tctx.Thinking.Budget = 1024 },
	} {
		if mk(edit) == base {
			t.Errorf("%s does not change the key", name)
		}
	}
	if mk(func(*call) {}) != base {
		t.Fatal("the key is not stable")
	}
}
