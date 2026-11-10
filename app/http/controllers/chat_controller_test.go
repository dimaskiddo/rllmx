package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/account"
	"rllmx/app/gateway/pipeline"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/llm/executor"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

const (
	chatNode  = "openai-compatible-c1"
	chatModel = chatNode + "/gpt-x"
	chatReply = `{"id":"chatcmpl-abc12345","object":"chat.completion","created":1,"model":"gpt-x","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
)

type keyed struct{}

func (keyed) Session(*nethttp.Request) bool { return false }
func (keyed) APIKey(*nethttp.Request) identity.KeyResult {
	return identity.KeyResult{Status: identity.KeyValid, Caller: identity.Caller{ID: "key-7", Name: "ci"}}
}
func (keyed) RequireLogin(context.Context) bool  { return true }
func (keyed) RequireAPIKey(context.Context) bool { return true }

type chatRig struct {
	t    *testing.T
	up   *tests.Upstream
	rows *usage.Sink
	pipe func() (*pipeline.Pipeline, error)
	h    nethttp.Handler
}

func newChatRig(t *testing.T, limit int64, steps ...tests.Reply) *chatRig {
	t.Helper()
	return newChatRigWith(t, limit, nil, steps...)
}

// newChatRigWith is newChatRig with a chance to change the pipeline's
// dependencies before it is built.
func newChatRigWith(t *testing.T, limit int64, edit func(*pipeline.Deps), steps ...tests.Reply) *chatRig {
	t.Helper()
	up := tests.NewUpstream(t, steps...)
	st := store.Migrated(t)
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	node := repository.Node{ID: chatNode, Type: upstream.NodeTypeOpenAI, Name: chatNode, Data: `{"baseUrl":"` + up.URL + `","apiType":"chat"}`}
	if _, err := st.Nodes().CreateUnique(context.Background(), node, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: chatNode, AuthType: "apikey", Name: "k1", Fields: map[string]string{"apiKey": `"k1"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	log := &tests.Log{}
	sink := &usage.Sink{}
	deps := pipeline.Deps{
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(routing.NewRepositorySource(st.Store), nil)},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: tests.Settings{}, Log: log}),
		Reporter: account.NewReporter(st.Connections(), log, nil),
		Executor: &executor.Default{Client: nethttp.DefaultClient, Sleep: func(time.Duration) <-chan time.Time {
			c := make(chan time.Time, 1)
			c <- time.Now()
			return c
		}},
		Nodes: st.Nodes(), Registry: reg, Caps: caps, Usage: sink,
		Timers: stream.Timers{FirstChunk: 300 * time.Millisecond, Stall: 300 * time.Millisecond, UsageWait: 50 * time.Millisecond},
		Log:    log,
	}
	if edit != nil {
		edit(&deps)
	}
	p, err := pipeline.New(deps)
	if err != nil {
		t.Fatal(err)
	}
	r := &chatRig{t: t, up: up, rows: sink, pipe: func() (*pipeline.Pipeline, error) { return p, nil }}
	r.mount(limit)
	return r
}

func (r *chatRig) mount(limit int64) {
	route := tests.NewRoute(r.t)
	g := apphttp.NewGuard(keyed{}, true)
	route.SetGlobalMiddleware([]contractshttp.Middleware{
		&middleware.RequestID{}, &middleware.BodyLimit{ClientMax: limit}, &middleware.Guard{Guard: g},
	})
	c := &ChatController{Pipe: func() (*pipeline.Pipeline, error) { return r.pipe() }, Log: &tests.Log{}}
	for _, path := range []string{"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/responses/compact", "/v1beta/models/*path", "/v1/api/chat"} {
		g.Handle(route, apphttp.ClassLLM, nethttp.MethodPost, path, c.Serve)
	}
	g.Handle(route, apphttp.ClassLLM, nethttp.MethodPost, "/v1/messages/count_tokens", c.CountTokens)
	r.h = route
}

func (r *chatRig) post(ctx context.Context, body string, header map[string]string) *httptest.ResponseRecorder {
	return r.postTo(ctx, "/v1/chat/completions", body, header)
}

func (r *chatRig) postTo(ctx context.Context, path, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(nethttp.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
	req.RemoteAddr = "203.0.113.9:5000"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-Id", "rid-chat-1")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func TestChatAnswersJSONAndTheRowNamesTheCaller(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	rec := r.post(context.Background(), `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("%v", rec.Header())
	}
	list := r.rows.All()
	if len(list) != 1 || list[0].APIKeyID != "key-7" || list[0].RequestID != "rid-chat-1" || list[0].Status != 200 {
		t.Fatalf("%+v", list)
	}
}

func TestChatStreamsSSE(t *testing.T) {
	chunks := []string{
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}` + "\n\n",
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "text/event-stream", Chunks: chunks})
	rec := r.post(context.Background(), `{"model":"`+chatModel+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`, nil)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") ||
		!strings.Contains(body, `"content":"hel"`) || !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), body)
	}
	if n := strings.Count(body, "data: [DONE]"); n != 1 {
		t.Fatalf("the stream ended %d times: %s", n, body)
	}
	if list := r.rows.All(); len(list) != 1 || !list[0].Stream {
		t.Fatalf("%+v", list)
	}
}

func TestChatAnswersTheCLIProbeWithoutAnUpstream(t *testing.T) {
	r := newChatRig(t, 1<<20)
	rec := r.post(context.Background(), `{"model":"nothing/here","messages":[{"role":"user","content":"Warmup"}]}`,
		map[string]string{"User-Agent": "claude-cli/1.0"})
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "choices.0.message.content").String() != "CLI Command Execution: Clear Terminal" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("the probe reached an upstream")
	}
}

func TestChatRefusesABodyOverTheCap(t *testing.T) {
	r := newChatRig(t, 64)
	rec := r.post(context.Background(), `{"model":"`+chatModel+`","messages":[{"role":"user","content":"`+strings.Repeat("a", 200)+`"}]}`, nil)
	if rec.Code != 413 || len(r.up.Calls()) != 0 || len(r.rows.All()) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestChatBadJSONIsAnOpenAIError(t *testing.T) {
	r := newChatRig(t, 1<<20)
	rec := r.post(context.Background(), `{not json`, nil)
	if rec.Code != 400 || gjson.Get(rec.Body.String(), "error.message").String() == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if list := r.rows.All(); len(list) != 1 || list[0].Status != 400 {
		t.Fatalf("%+v", list)
	}
}

func TestChatPipelineBuildFailureIs503InTheClientsShape(t *testing.T) {
	r := newChatRig(t, 1<<20)
	r.pipe = func() (*pipeline.Pipeline, error) { return nil, errors.New("stores closed") }
	rec := r.post(context.Background(), `{"model":"m","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rec.Code != 503 || gjson.Get(rec.Body.String(), "error.message").String() == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "stores closed") {
		t.Fatalf("internal detail reached the client: %s", rec.Body.String())
	}
}

func TestChatWithoutACallerIs401(t *testing.T) {
	c := &ChatController{Pipe: func() (*pipeline.Pipeline, error) { return nil, errors.New("must not be asked") }}
	route := tests.NewRoute(t)
	route.SetGlobalMiddleware(nil)
	route.Post("/v1/chat/completions", c.Serve)
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	route.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestChatClientDisconnectCancelsTheUpstreamCall(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{Hang: true})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- r.post(ctx, `{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`, nil)
	}()
	select {
	case rec := <-done:
		if rec.Body.Len() != 0 || rec.Code != 200 || len(rec.Header()["Content-Type"]) != 0 {
			t.Fatalf("a closed client was written to: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler kept waiting for the upstream after the client left")
	}
	if list := r.rows.All(); len(list) != 1 || list[0].Status != 499 {
		t.Fatalf("%+v", list)
	}
}

func TestChatRefusesAChunkedBodyOverTheCap(t *testing.T) {
	r := newChatRig(t, 64)
	body := `{"model":"` + chatModel + `","messages":[{"role":"user","content":"` + strings.Repeat("a", 200) + `"}]}`
	req := httptest.NewRequest(nethttp.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.ContentLength = -1
	req.RemoteAddr = "203.0.113.9:5000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	if rec.Code != 413 || len(r.up.Calls()) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
