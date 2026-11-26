package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/account"
	"rllmx/app/llm/executor"
	"rllmx/app/llm/executor/mux"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

const (
	nodeID   = "openai-compatible-t1"
	claudeID = "anthropic-compatible-t2"
	prices   = `{"models":{"gpt-x":{"input":1,"output":2}}}`
)

const (
	chatBody   = `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	chatStream = `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	okJSON     = `{"id":"chatcmpl-abc12345","object":"chat.completion","created":1,"model":"gpt-x","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
)

func sseChunks() []string {
	c := func(delta, tail string) string {
		return `data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":` + delta + tail + `}]}` + "\n\n"
	}
	return []string{
		c(`{"role":"assistant","content":"hel"}`, ""),
		c(`{"content":"lo"}`, ""),
		c(`{}`, `,"finish_reason":"stop"`),
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	}
}

type spy struct {
	Reporter
	succeeded atomic.Int32
}

// healthRec is one outcome the pipeline gave the tracker.
type healthRec struct {
	provider string
	ok       bool
	latency  time.Duration
}

type healthSpy struct {
	mu   sync.Mutex
	recs []healthRec
}

func (h *healthSpy) Record(provider string, ok bool, latency time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.recs = append(h.recs, healthRec{provider: provider, ok: ok, latency: latency})
}

func (h *healthSpy) Open(string) bool { return false }

func (h *healthSpy) all() []healthRec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]healthRec(nil), h.recs...)
}

func (s *spy) Succeed(ctx context.Context, id, model string, at time.Time) error {
	s.succeeded.Add(1)
	return s.Reporter.Succeed(ctx, id, model, at)
}

type source struct {
	combos []string
	// comboMembers is the member list per combo, for expansion.
	comboMembers []repository.Combo
}

func (s source) Aliases(context.Context) (map[string]string, error) {
	return map[string]string{
		"m":   `"` + nodeID + `/gpt-x"`,
		"cm":  `"` + claudeID + `/claude-x"`,
		"cur": `"cur/claude-4.5-sonnet"`,
	}, nil
}
func (s source) Nodes(context.Context) ([]routing.Node, error) { return nil, nil }
func (s source) ComboNames(context.Context) ([]string, error)  { return s.combos, nil }
func (s source) Combos(context.Context) (map[string]repository.Combo, error) {
	out := map[string]repository.Combo{}
	for _, c := range s.comboMembers {
		out[c.Name] = c
	}
	return out, nil
}
func (s source) ActiveProviders(context.Context) ([]string, error) {
	return nil, nil
}

type rig struct {
	t    *testing.T
	p    *Pipeline
	st   *store.Store
	up   *tests.Upstream
	rows *usage.Sink
	rep  *spy
	hlt  *healthSpy
	flg  *flightSpy
	log  *tests.Log
}

func instant(time.Duration) <-chan time.Time {
	c := make(chan time.Time, 1)
	c <- time.Now()
	return c
}

func newRig(t *testing.T, steps ...tests.Reply) *rig {
	t.Helper()
	up := tests.NewUpstream(t, steps...)
	st := store.Migrated(t)
	ctx := context.Background()

	forced := `{"id":"forced","category":"apikey","display":{"name":"F"},"forceStream":true,"transport":{"baseUrl":"` + up.URL + `/v1/chat/completions","format":"openai"},"models":[{"id":"fm"},{"id":"fm-img","kind":"image"}]}`
	gem := `{"id":"gem","alias":"gem","category":"apikey","display":{"name":"G"},"transport":{"baseUrl":"` + up.URL + `/v1beta/models","format":"gemini"},"auth":{"scheme":"raw","header":"x-goog-api-key"},"models":[{"id":"g-vision"}]}`
	sub := `{"id":"sub","category":"oauth","display":{"name":"S"},"transport":{"baseUrl":"` + up.URL + `/v1/messages","format":"claude"},` +
		`"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/authorize","tokenUrl":"https://a.example/token","scope":"s","redirect":"local","exchange":"json",` +
		`"refresh":{"encoding":"json","leadSeconds":300},"disguise":true,"urlSuffix":"?beta=true",` +
		`"identity":{"User-Agent":"claude-cli/9.9.9 (external, sdk-cli)","X-App":"cli"},` +
		`"beta":{"base":["base-a","oauth-x","redact-y"],"heavy":["heavy-1"],"heavyPrefixes":["claude-opus","claude-sonnet"],"summary":"redact-y"},` +
		`"session":{"name":"x-claude-code-session-id"}},"models":[{"id":"claude-x"},{"id":"claude-opus-5"}]}`
	subx := `{"id":"subx","category":"oauth","display":{"name":"X"},"transport":{"baseUrl":"` + up.URL + `/v1/chat/completions","format":"openai"},` +
		`"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/authorize","tokenUrl":"https://a.example/token","scope":"s","redirect":"local","exchange":"json",` +
		`"refresh":{"encoding":"json","leadSeconds":300},"identity":{"originator":"codex_cli_rs","User-Agent":"codex_cli_rs/9.9.9","version":"9.9.9"},` +
		`"session":{"name":"session_id","keyed":true},"account":{"name":"ChatGPT-Account-ID","from":["workspaceId","chatgptAccountId","accountId"]}},"models":[{"id":"x1"}]}`
	free := `{"id":"free","category":"free","noAuth":true,"display":{"name":"F"},"transport":{"baseUrl":"` + up.URL + `/v1/chat/completions","format":"openai"},"models":[{"id":"fm"}]}`
	wire := `{"id":"wire","category":"apikey","display":{"name":"W"},"transport":{"baseUrl":"` + up.URL + `/v1/chat/completions","format":"openai","urlSuffix":"?v=1",` +
		`"headers":{"X-Title":"rllmx"},"retry":{"429":{"attempts":1,"delayMs":10}},"reasoningInject":"all"},` +
		`"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"short","upstreamId":"vendor/long-name"}]}`
	acct := `{"id":"acct","category":"freeTier","display":{"name":"A"},"transport":{"baseUrl":"` + up.URL + `/accounts/{accountId}/chat/completions","format":"openai"},` +
		`"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"am"}]}`
	cln := `{"id":"cln","category":"oauth","display":{"name":"N"},"transport":{"baseUrl":"` + up.URL + `/api/v1/chat/completions","format":"openai","quirks":{"clineEnvelope":true}},` +
		`"auth":{"scheme":"workos","header":"Authorization"},"models":[{"id":"cl-1"}]}`
	glb := `{"id":"glb","category":"oauth","display":{"name":"B"},"transport":{"baseUrl":"{baseUrl}/api/v4/chat/completions","format":"openai"},"models":[{"id":"gm1"}]}`
	cbn := `{"id":"cbn","category":"oauth","display":{"name":"CB"},"executor":"codebuddy-cn","forceStream":true,` +
		`"transport":{"baseUrl":"` + up.URL + `/v2/chat/completions","format":"openai","thinkingFormat":"openai"},` +
		`"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"cb-1"}]}`
	mmx := `{"id":"mmx","category":"oauth","display":{"name":"MM"},"executor":"minimax-code",` +
		`"transport":{"baseUrl":"` + up.URL + `/mavis/api/v1/llm/v1/messages","format":"claude","headers":{"x-api-key":"sk-xxx","anthropic-version":"2023-06-01"}},` +
		`"oauth":{"flow":"device","dialect":"minimax","deviceUrl":"https://a.example/device","tokenUrl":"https://a.example/token","scope":"s"},` +
		`"models":[{"id":"mm-1"}]}`
	gcl := `{"id":"gcl","category":"oauth","display":{"name":"GC"},"executor":"gemini-cli",` +
		`"transport":{"baseUrl":"` + up.URL + `/v1internal","format":"gemini-cli","headers":{"X-Rllmx-User-Agent":"GeminiCLI/0.34.0/{model} (router; router; terminal)"}},` +
		`"oauth":{"flow":"code-pkce","noPkce":true,"authorizeUrl":"https://a.example/a","tokenUrl":"https://a.example/t","scope":"s","redirect":"local","exchange":"form","refresh":{"encoding":"form","leadSeconds":300}},` +
		`"models":[{"id":"gm-1"}]}`
	agy := `{"id":"antigravity","category":"oauth","display":{"name":"AG"},"executor":"antigravity",` +
		`"transport":{"baseUrl":"` + up.URL + `/v1internal","format":"gemini-cli"},` +
		`"oauth":{"flow":"code-pkce","noPkce":true,"authorizeUrl":"https://a.example/a","tokenUrl":"https://a.example/t","scope":"s","redirect":"local","exchange":"form","refresh":{"encoding":"form","leadSeconds":300}},` +
		`"models":[{"id":"claude-opus-5-5-high"}]}`
	cdx := `{"id":"cdx","category":"apikey","display":{"name":"C"},"executor":"codex","forceStream":true,` +
		`"transport":{"baseUrl":"` + up.URL + `/codex/responses","format":"openai-responses","quirks":{"inlineImages":true}},` +
		`"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"cm","levels":["low","medium","high","xhigh"]},{"id":"cm-vision","levels":["low","medium"]},{"id":"cl","lite":true,"levels":["low","medium","high"]}]}`
	grk := `{"id":"grk","category":"apikey","display":{"name":"G"},"executor":"grok-cli","forceStream":true,` +
		`"transport":{"baseUrl":"` + up.URL + `/grok/responses","format":"openai-responses","headers":{"x-grok-client-identifier":"grok-shell"}},` +
		`"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"gm","levels":["low","high"]},{"id":"gn"}]}`
	cop := `{"id":"cop","category":"oauth","display":{"name":"P"},"executor":"github",` +
		`"transport":{"baseUrl":"` + up.URL + `/chat/completions","responsesUrl":"` + up.URL + `/responses","messagesUrl":"` + up.URL + `/v1/messages","format":"openai",` +
		`"headers":{"anthropic-version":"2023-06-01"}},` +
		`"oauth":{"flow":"device","deviceUrl":"https://a.example/device","tokenUrl":"https://a.example/token","scope":"s","hook":"github","mint":"copilot",` +
		`"endpoints":{"copilotToken":"https://a.example/copilot"},"headers":{"copilot-integration-id":"vscode-chat","X-Initiator":"user"}},` +
		`"models":[{"id":"cm-chat"},{"id":"cm-claude","targetFormat":"claude"},{"id":"cm-resp","targetFormat":"openai-responses"}]}`
	dev := func(id, extra string) string {
		return `{"id":"` + id + `","category":"oauth","display":{"name":"D"},"transport":{"baseUrl":"` + up.URL + `/chat/completions","format":"openai"},` +
			`"oauth":{"flow":"device","deviceUrl":"https://a.example/device","tokenUrl":"https://a.example/token","scope":"s"` + extra + `},"models":[{"id":"m1"}]}`
	}
	kim := dev("kim", `,"headers":{"X-Msh-Platform":"rllmx"},"callHeaders":{"X-Msh-Device-Id":"{deviceId}"},"refresh":{"encoding":"form","leadSeconds":300},"account":{"name":"X-Msh-Device-Id","from":["deviceId"]}`)
	mus := dev("mus", `,"headers":{"x-api-version":"1.0.0"}`)
	kc := dev("kc", `,"account":{"name":"X-Kilocode-OrganizationID","from":["orgId"]}`)
	qwn := `{"id":"qwn","category":"apikey","display":{"name":"Q"},"transport":{"baseUrl":"` + up.URL + `/v1/chat/completions","format":"openai","thinkingFormat":"qwen",` +
		`"lanes":{"claude":{"baseUrl":"` + up.URL + `/v1/messages","thinkingFormat":"claude-adaptive"}}},` +
		`"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"qm"}]}`
	lane := `{"id":"lane","category":"oauth","display":{"name":"L"},"transport":{"baseUrl":"` + up.URL + `/v1/messages","format":"claude","urlSuffix":"?beta=true",` +
		`"headers":{"anthropic-version":"2023-06-01"},"lanes":{` +
		`"openai":{"baseUrl":"` + up.URL + `/v1/chat/completions","auth":{"scheme":"bearer","header":"Authorization"}},` +
		`"openai-responses":{"baseUrl":"` + up.URL + `/v1/responses","auth":{"scheme":"raw","header":"x-lane-key"},"headers":{"X-Lane":"resp"}}}},` +
		`"auth":{"scheme":"raw","header":"x-api-key"},` +
		`"oauth":{"flow":"device","deviceUrl":"https://a.example/device","tokenUrl":"https://a.example/token","scope":"s","headers":{"X-Login":"1"}},` +
		`"models":[{"id":"m1"}]}`
	cur := `{"id":"cur","category":"oauth","display":{"name":"CU"},"executor":"cursor","forceStream":true,` +
		`"transport":{"baseUrl":"` + up.URL + `/agent.v1.AgentService/Run","format":"cursor"},` +
		`"models":[{"id":"claude-4.5-sonnet"}]}`
	reg, err := upstream.Load(fstest.MapFS{
		"data/cur.json": {Data: []byte(cur)},
		"data/kim.json": {Data: []byte(kim)}, "data/mus.json": {Data: []byte(mus)}, "data/kc.json": {Data: []byte(kc)}, "data/lane.json": {Data: []byte(lane)}, "data/qwn.json": {Data: []byte(qwn)},
		"data/cop.json": {Data: []byte(cop)}, "data/cln.json": {Data: []byte(cln)}, "data/glb.json": {Data: []byte(glb)},
		"data/gcl.json": {Data: []byte(gcl)}, "data/antigravity.json": {Data: []byte(agy)}, "data/cbn.json": {Data: []byte(cbn)}, "data/mmx.json": {Data: []byte(mmx)},
		"data/cdx.json": {Data: []byte(cdx)}, "data/grk.json": {Data: []byte(grk)},
		"data/forced.json": {Data: []byte(forced)}, "data/gem.json": {Data: []byte(gem)}, "data/sub.json": {Data: []byte(sub)}, "data/subx.json": {Data: []byte(subx)},
		"data/wire.json": {Data: []byte(wire)}, "data/acct.json": {Data: []byte(acct)}, "data/free.json": {Data: []byte(free)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	table, err := upstream.NewTable([]byte(prices), reg)
	if err != nil {
		t.Fatal(err)
	}
	for id, typ := range map[string]string{nodeID: upstream.NodeTypeOpenAI, claudeID: upstream.NodeTypeAnthropic} {
		data := `{"baseUrl":"` + up.URL + `","apiType":"chat"}`
		if _, err := st.Nodes().CreateUnique(ctx, repository.Node{ID: id, Type: typ, Name: id, Data: data}, ""); err != nil {
			t.Fatal(err)
		}
	}
	log := &tests.Log{}
	rep := &spy{Reporter: account.NewReporter(st.Connections(), log, nil)}
	rows := &usage.Sink{}
	hlt := &healthSpy{}
	flg := &flightSpy{}
	p, err := New(Deps{
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(source{}, nil)},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: tests.Settings{}, Log: log}),
		Reporter: rep,
		Executor: mux.New(&executor.Default{Client: http.DefaultClient, Sleep: instant}, mux.Options{}),
		Nodes:    st.Nodes(), Registry: reg, Caps: caps, Prices: table, Usage: rows, Health: hlt, Inflight: flg,
		Timers: stream.Timers{FirstChunk: 300 * time.Millisecond, Stall: 300 * time.Millisecond, UsageWait: 50 * time.Millisecond},
		Log:    log,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, p: p, st: st, up: up, rows: rows, rep: rep, hlt: hlt, flg: flg, log: log}
}

func (r *rig) conn(provider, key string) repository.Connection {
	r.t.Helper()
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: provider, AuthType: "apikey", Name: key, Fields: map[string]string{"apiKey": `"` + key + `"`},
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

func (r *rig) do(path, body string) (Output, *httptest.ResponseRecorder) {
	r.t.Helper()
	before := len(r.rows.All())
	out, rec := r.run(path, body)
	if n := len(r.rows.All()) - before; n != 1 {
		r.t.Errorf("a request left %d usage rows, want 1", n)
	}
	return out, rec
}

func (r *rig) run(path, body string) (Output, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	in := Input{Path: path, Body: []byte(body), Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-1"}
	return r.p.Handle(context.Background(), in, open), rec
}

func (r *rig) last() analytics.Record {
	rows := r.rows.All()
	return rows[len(rows)-1]
}

func auth(c tests.Call) string { return c.Header.Get("Authorization") }

// A provider that needs no credential is served through a virtual account, so
// a request reaches it with nothing stored and no account locked.
func TestAProviderThatNeedsNoCredentialIsServed(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	out, _ := r.do("/v1/chat/completions", `{"model":"free/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	calls := r.up.Calls()
	if len(calls) != 1 || calls[0].Path != "/v1/chat/completions" {
		t.Fatalf("calls %v", calls)
	}
	if got := auth(calls[0]); got != "" {
		t.Fatalf("a no-credential provider carried %q", got)
	}
	if row := r.last(); row.ConnectionID != "noauth" {
		t.Fatalf("the row names %q", row.ConnectionID)
	}
}

func TestNonStreamAnswerAndRow(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	conn := r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if gjson.GetBytes(out.Body, "usage.prompt_tokens").Int() != 2010 || gjson.GetBytes(out.Body, "usage.total_tokens").Int() != 2015 {
		t.Fatalf("the client buffer is missing: %s", out.Body)
	}
	call := r.up.Calls()[0]
	if call.Path != "/chat/completions" || auth(call) != "Bearer k1" || gjson.GetBytes(call.Body, "model").String() != "gpt-x" {
		t.Fatalf("%s %s %s", call.Path, auth(call), call.Body)
	}
	if gjson.GetBytes(call.Body, "stream").Bool() || call.Header.Get("Accept") != "" {
		t.Fatalf("a missing stream must mean no stream: %s %v", call.Body, call.Header)
	}
	row := r.last()
	if row.PromptTokens != 10 || row.CompletionTokens != 5 || row.Cost != 0.00002 {
		t.Fatalf("the row must hold the raw counts and the price: %+v", row)
	}
	if row.ConnectionID != conn.ID || row.Provider != nodeID || row.Model != "gpt-x" || row.SourceFormat != "openai" ||
		row.APIKeyID != "key-1" || row.Status != 200 || row.ErrorClass != "none" || row.Attempt != 1 || row.Stream || row.Endpoint != "/v1/chat/completions" {
		t.Fatalf("%+v", row)
	}
	if r.rep.succeeded.Load() != 1 {
		t.Fatal("an accepted answer must clear the locks")
	}
}

func TestAnswerWithoutUsageIsEstimated(t *testing.T) {
	body := strings.Replace(okJSON, `,"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}`, "", 1)
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: body})
	r.conn(nodeID, "k1")
	r.do("/v1/chat/completions", chatBody)
	if row := r.last(); !row.Estimated || row.PromptTokens == 0 {
		t.Fatalf("%+v", row)
	}
}

func TestStreamRelaysAndRecordsUsage(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn(nodeID, "k1")
	out, rec := r.do("/v1/chat/completions", chatStream)
	if !out.Streamed || out.Status != 200 {
		t.Fatalf("%+v", out)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"content":"hel"`) || !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("%s", body)
	}
	if !strings.Contains(body, `"prompt_tokens":2010`) {
		t.Fatalf("the client buffer is missing from the stream: %s", body)
	}
	row := r.last()
	if row.PromptTokens != 10 || row.CompletionTokens != 5 || !row.Stream || row.Status != 200 || row.TTFBMs < 0 {
		t.Fatalf("%+v", row)
	}
	if call := r.up.Calls()[0]; call.Header.Get("Accept") != "text/event-stream" || !gjson.GetBytes(call.Body, "stream").Bool() {
		t.Fatalf("%v %s", call.Header, call.Body)
	}
	if r.rep.succeeded.Load() != 1 {
		t.Fatal("a stream that delivered must clear the locks")
	}
}

func TestClaudeClientToOpenAIUpstream(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/messages", `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || gjson.GetBytes(out.Body, "type").String() != "message" || gjson.GetBytes(out.Body, "content.0.text").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if r.last().SourceFormat != "claude" {
		t.Fatalf("%+v", r.last())
	}
}

func TestGeminiClientActionsDecideStreaming(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON}, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn(nodeID, "k1")
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	out, _ := r.do("/v1beta/models/m:generateContent", body)
	if out.Status != 200 || gjson.GetBytes(out.Body, "candidates.0.content.parts.0.text").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	out, rec := r.do("/v1beta/models/m:streamGenerateContent", body)
	if !out.Streamed || !strings.Contains(rec.Body.String(), `"candidates"`) {
		t.Fatalf("%+v %s", out, rec.Body.String())
	}
	if r.do1("/v1beta/models/m:embedContent", body) != 404 {
		t.Fatal("an action that is not a chat call must be 404")
	}
}

func (r *rig) do1(path, body string) int {
	out, _ := r.do(path, body)
	return out.Status
}

func TestAClientThatWantsJSONFromAStreamOnlyProviderGetsOneAnswer(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || out.Streamed || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if !gjson.GetBytes(r.up.Calls()[0].Body, "stream").Bool() {
		t.Fatal("the upstream must have been asked for a stream")
	}
	if row := r.last(); row.PromptTokens != 10 || row.Stream {
		t.Fatalf("%+v", row)
	}
}

func TestClaudeNodeUsesItsOwnEndpointAndKeyHeader(t *testing.T) {
	msg := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"hey"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: msg})
	r.conn(claudeID, "ck")
	out, _ := r.do("/v1/chat/completions", `{"model":"cm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hey" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if call.Path != "/messages" || call.Header.Get("x-api-key") != "ck" || call.Header.Get("anthropic-version") == "" || auth(call) != "" {
		t.Fatalf("%s %v", call.Path, call.Header)
	}
}

// An Anthropic-compatible node implements the context-editing field a Claude
// Code client sends, so the request keeps it; only a gateway that rejects it
// may drop it.
func TestAnAnthropicNodeKeepsTheClientsContextManagement(t *testing.T) {
	msg := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"text","text":"hey"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":2}}`
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: msg})
	r.conn(claudeID, "ck")
	body := `{"model":"cm","max_tokens":9,"context_management":{"edits":[]},"messages":[{"role":"user","content":"hi"}]}`
	if out, _ := r.do("/v1/messages", body); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if call := r.up.Calls()[0]; !gjson.GetBytes(call.Body, "context_management").Exists() {
		t.Fatalf("the client's context_management was dropped: %s", call.Body)
	}
}

func TestRateLimitedAccountFallsBackToTheNext(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"rate limit reached"}}`}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	out, _ := r.do("/v1/chat/completions", chatBody)
	calls := r.up.Calls()
	if out.Status != 200 || len(calls) != 2 || auth(calls[0]) == auth(calls[1]) {
		t.Fatalf("%d after %d calls", out.Status, len(calls))
	}
	if row := r.last(); row.Attempt != 2 || row.Status != 200 {
		t.Fatalf("%+v", row)
	}
}

func TestARequestScopedErrorLocksNothing(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 400, Body: `{"error":{"message":"bad parameter"}}`}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 400 || !strings.Contains(string(out.Body), "bad parameter") || len(r.up.Calls()) != 1 {
		t.Fatalf("%d %s after %d calls", out.Status, out.Body, len(r.up.Calls()))
	}
	if row := r.last(); row.ErrorClass != "invalid_request" || row.Status != 400 {
		t.Fatalf("%+v", row)
	}
	if out, _ = r.do("/v1/chat/completions", chatBody); out.Status != 200 {
		t.Fatalf("the account was locked by a request error: %d %s", out.Status, out.Body)
	}
}

func TestEveryAccountIsTriedOnce(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"rate limit"}}`})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 429 || len(r.up.Calls()) != 2 {
		t.Fatalf("%d after %d calls", out.Status, len(r.up.Calls()))
	}
}

func TestAServerErrorLocksTheAccountAndTheNextRequestSeesIt(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 500, Body: `{"error":{"message":"boom"}}`})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 500 || r.last().ErrorClass != "upstream_5xx" {
		t.Fatalf("%d %+v", out.Status, r.last())
	}
	out, _ = r.do("/v1/chat/completions", chatBody)
	if out.Status != 503 || out.Header.Get("Retry-After") == "" || gjson.GetBytes(out.Body, "retryAfter").String() == "" {
		t.Fatalf("%d %v %s", out.Status, out.Header, out.Body)
	}
	if !strings.Contains(string(out.Body), "reset after") || r.last().ErrorClass != "no_account" || len(r.up.Calls()) != 1 {
		t.Fatalf("%s %+v", out.Body, r.last())
	}
}

func TestARefusedCredentialIsNotPassedToTheClientAs401(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 401, Body: `{"error":{"message":"invalid api key"}}`})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 502 || r.last().ErrorClass != "auth" || r.last().Status != 502 {
		t.Fatalf("%d %+v", out.Status, r.last())
	}
}

func TestNoConnectionIs404InTheClientsShape(t *testing.T) {
	r := newRig(t)
	out, _ := r.do("/v1/messages", `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 404 || gjson.GetBytes(out.Body, "type").String() != "error" || r.last().ErrorClass != "no_account" {
		t.Fatalf("%d %s %+v", out.Status, out.Body, r.last())
	}
}

func TestAComboNameIsServedByItsMembers(t *testing.T) {
	// forced is the rig's stream-only provider, so a non-stream client still gets
	// one answer from it.
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.p.d.Resolver = &routing.Resolver{Registry: r.p.d.Registry, Snapshot: routing.NewSnapshot(source{
		combos:       []string{"mycombo"},
		comboMembers: []repository.Combo{{Name: "mycombo", Models: []string{"forced/fm"}}},
	}, nil)}
	out, _ := r.do("/v1/chat/completions", `{"model":"mycombo","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.ComboName != "mycombo" || row.Model != "fm" || row.Provider != "forced" {
		t.Fatalf("%+v", row)
	}
}

func TestABadRequestIsAnsweredAndRecorded(t *testing.T) {
	r := newRig(t)
	for name, c := range map[string]struct{ path, body string }{
		"not json":      {"/v1/chat/completions", `{`},
		"no model":      {"/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`},
		"control model": {"/v1/chat/completions", "{\"model\":\"a\\u0007b\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"},
		"unknown path":  {"/v1/nothing", `{}`},
	} {
		out, _ := r.do(c.path, c.body)
		if out.Status < 400 || out.Status >= 500 || len(out.Body) == 0 {
			t.Errorf("%s: %d %s", name, out.Status, out.Body)
		}
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("a bad request reached the upstream")
	}
}

func TestAPageOnASuccessStatusClearsAndLocksNothing(t *testing.T) {
	page := "<html><head><title>Just a moment</title></head><body>wait</body></html>"
	r := newRig(t, tests.Reply{ContentType: "text/html", Body: page}, tests.Reply{ContentType: "text/html", Body: page}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if out.Status != 502 || !strings.Contains(string(out.Body), "Just a moment") || strings.Contains(string(out.Body), "<") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if out, _ = r.do("/v1/chat/completions", chatStream); out.Status != 502 || out.Streamed {
		t.Fatalf("%d %+v", out.Status, out)
	}
	if r.rep.succeeded.Load() != 0 {
		t.Fatal("a page cleared the account's locks")
	}
	if out, _ = r.do("/v1/chat/completions", chatBody); out.Status != 200 {
		t.Fatalf("the account was locked by a page: %d", out.Status)
	}
}

func TestAnEmptyAnswerIsNotAccepted(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: ""}, tests.Reply{ContentType: "application/json", Body: `{"choices":[]}`})
	r.conn(nodeID, "k1")
	for range 2 {
		if out, _ := r.do("/v1/chat/completions", chatBody); out.Status != 502 {
			t.Fatalf("%d %s", out.Status, out.Body)
		}
	}
	if r.rep.succeeded.Load() != 0 {
		t.Fatal("an empty answer cleared the locks")
	}
}

func TestAStreamThatNeverStartsFallsBackToTheNextAccount(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Silent: true}, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	out, rec := r.do("/v1/chat/completions", chatStream)
	if !out.Streamed || !strings.Contains(rec.Body.String(), `"content":"hel"`) || len(r.up.Calls()) != 2 {
		t.Fatalf("%+v %s after %d calls", out, rec.Body.String(), len(r.up.Calls()))
	}
	if strings.Contains(rec.Body.String(), "error") || r.last().Attempt != 2 {
		t.Fatalf("an error frame leaked to the client or the attempt is wrong: %s %+v", rec.Body.String(), r.last())
	}
}

func TestAStreamThatBreaksAfterBytesIsNotRetried(t *testing.T) {
	chunks := sseChunks()[:2]
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: chunks, Pause: 2 * time.Second}, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	out, rec := r.do("/v1/chat/completions", chatStream)
	if !out.Streamed || len(r.up.Calls()) != 1 {
		t.Fatalf("%+v after %d calls", out, len(r.up.Calls()))
	}
	if !strings.Contains(rec.Body.String(), `"content":"hel"`) || !strings.Contains(rec.Body.String(), "stopped sending data") {
		t.Fatalf("%s", rec.Body.String())
	}
	if row := r.last(); row.Status != 504 || row.ErrorClass != "stream_stall" || row.Attempt != 1 {
		t.Fatalf("%+v", row)
	}
	if r.rep.succeeded.Load() != 0 {
		t.Fatal("a broken stream cleared the locks")
	}
	if recs := r.hlt.all(); len(recs) != 1 || recs[0].ok {
		t.Fatalf("a stream that broke after it opened was not counted: %+v", recs)
	}
}

func TestANetworkFailureShowsNeitherAddressNorKey(t *testing.T) {
	r := newRig(t)
	r.conn(nodeID, "sk-very-secret")
	r.up.Close()
	out, _ := r.do("/v1/chat/completions", chatBody)
	body := string(out.Body)
	if out.Status != 502 || strings.Contains(body, "127.0.0.1") || strings.Contains(body, "sk-very-secret") || r.last().ErrorClass != "network" {
		t.Fatalf("%d %s %+v", out.Status, body, r.last())
	}
}

func TestAnUpstreamPageInAnErrorBodyNeverReachesTheClient(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 500, ContentType: "text/html", Body: "<html><body><script>alert(1)</script>down</body></html>"})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/chat/completions", chatBody)
	if strings.Contains(string(out.Body), "<") {
		t.Fatalf("%s", out.Body)
	}
}

func TestConcurrentRequests(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if out, _ := r.run("/v1/chat/completions", chatBody); out.Status != 200 {
				t.Errorf("%d %s", out.Status, out.Body)
			}
		})
	}
	wg.Wait()
	if n := len(r.rows.All()); n != 16 {
		t.Fatalf("%d rows", n)
	}
}

func TestNewRefusesMissingDependencies(t *testing.T) {
	if _, err := New(Deps{}); err == nil {
		t.Fatal("an empty Deps was accepted")
	}
}

var _ = translate.FormatOpenAI

func TestAProvidersStallBoundReplacesTheSharedOne(t *testing.T) {
	r := newRig(t)
	r.p.d.Timers = stream.Timers{FirstChunk: 5 * time.Second, Stall: 9 * time.Second, UsageWait: time.Second}
	c := &call{p: r.p, dest: destination{entry: upstream.Entry{Transport: upstream.Transport{StallTimeoutMs: 120000}}}}
	if got := c.timers().Stall; got != 2*time.Minute {
		t.Fatalf("stall = %v, want the provider's two minutes", got)
	}
	if got := c.timers().FirstChunk; got != 5*time.Second {
		t.Fatalf("the first-chunk bound changed: %v", got)
	}
	c.dest.entry.Transport.StallTimeoutMs = 0
	if got := c.timers().Stall; got != 9*time.Second {
		t.Fatalf("stall = %v, want the shared value", got)
	}
}

func TestAMediaModelIsNotAServedChatModel(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", `{"model":"forced/fm-img","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != http.StatusNotFound {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}
