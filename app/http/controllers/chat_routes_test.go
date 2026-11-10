package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/pipeline"
	"rllmx/app/platform/cache"
	"rllmx/tests"
)

var userHi = map[string]string{
	"/v1/chat/completions":  `{"model":"` + chatModel + `","messages":[{"role":"user","content":"hi"}]}`,
	"/v1/messages":          `{"model":"` + chatModel + `","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`,
	"/v1/responses":         `{"model":"` + chatModel + `","input":"hi"}`,
	"/v1/responses/compact": `{"model":"` + chatModel + `","input":"hi"}`,
	"/v1beta/models/" + chatModel + ":generateContent": `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
	"/v1/api/chat": `{"model":"` + chatModel + `","stream":false,"messages":[{"role":"user","content":"hi"}]}`,
}

func TestEveryClientFormatGetsItsOwnShapeAndOneAgreeingRow(t *testing.T) {
	shape := map[string]string{
		"/v1/chat/completions":  "choices.0.message.content",
		"/v1/messages":          "content.0.text",
		"/v1/responses":         "output.0.content.0.text",
		"/v1/responses/compact": "output.0.content.0.text",
		"/v1beta/models/" + chatModel + ":generateContent": "candidates.0.content.parts.0.text",
		"/v1/api/chat": "message.content",
	}
	for path, body := range userHi {
		r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
		rec := r.postTo(context.Background(), path, body, nil)
		if rec.Code != 200 || gjson.Get(rec.Body.String(), shape[path]).String() != "hello" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
			continue
		}
		list := r.rows.All()
		if len(list) != 1 || list[0].PromptTokens != 10 || list[0].CompletionTokens != 5 || list[0].Provider != chatNode || list[0].Model != "gpt-x" {
			t.Errorf("%s: %+v", path, list)
		}
	}
}

func TestCompactIsServedLikeResponsesWithNoExtraField(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	rec := r.postTo(context.Background(), "/v1/responses/compact", userHi["/v1/responses/compact"], nil)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	calls := r.up.Calls()
	if len(calls) != 1 || strings.Contains(string(calls[0].Body), "compact") {
		t.Fatalf("%+v", calls)
	}
}

func TestOllamaStreamsNDJSONByDefaultAndHonoursAcceptAndExplicitStream(t *testing.T) {
	chunks := []string{
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}` + "\n\n",
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	sse := tests.Reply{ContentType: "text/event-stream", Chunks: chunks}
	json := tests.Reply{ContentType: "application/json", Body: chatReply}
	bare := `{"model":"` + chatModel + `","messages":[{"role":"user","content":"hi"}]}`
	streamed := `{"model":"` + chatModel + `","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	r := newChatRig(t, 1<<20, sse)
	rec := r.postTo(context.Background(), "/v1/api/chat", bare, nil)
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/x-ndjson") || strings.Count(rec.Body.String(), `"done":true`) != 1 {
		t.Fatalf("default: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}

	r = newChatRig(t, 1<<20, json)
	rec = r.postTo(context.Background(), "/v1/api/chat", bare, map[string]string{"Accept": "application/json"})
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") || gjson.Get(rec.Body.String(), "message.content").String() != "hello" {
		t.Fatalf("accept json: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}

	r = newChatRig(t, 1<<20, sse)
	rec = r.postTo(context.Background(), "/v1/api/chat", streamed, map[string]string{"Accept": "application/json"})
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/x-ndjson") {
		t.Fatalf("explicit stream with accept json: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestGeminiStreamsSSEForProviderAndModelAndRefusesOtherActions(t *testing.T) {
	chunks := []string{
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{"role":"assistant","content":"hel"}}]}` + "\n\n",
		`data: {"id":"chatcmpl-abc12345","object":"chat.completion.chunk","created":1,"model":"gpt-x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}` + "\n\n",
		"data: [DONE]\n\n",
	}
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "text/event-stream", Chunks: chunks})
	body := userHi["/v1beta/models/"+chatModel+":generateContent"]
	rec := r.postTo(context.Background(), "/v1beta/models/"+chatModel+":streamGenerateContent", body, nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.Contains(rec.Body.String(), `"text":"hel"`) {
		t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	for _, p := range []string{
		"/v1beta/models/" + chatModel + ":countTokens", "/v1beta/models/a/b/c:generateContent",
		"/v1beta/models/a%2Fb:generateContent", "/v1beta/models/../x:generateContent",
	} {
		rec := r.postTo(context.Background(), p, body, nil)
		if rec.Code == 200 {
			t.Errorf("%s was served", p)
		}
	}
	if rec := r.postTo(context.Background(), "/v1beta/models/"+chatModel+":countTokens", body, nil); rec.Code != 404 || gjson.Get(rec.Body.String(), "error.status").String() != "NOT_FOUND" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestCountTokensIsLocalAndLeavesNoRow(t *testing.T) {
	r := newChatRig(t, 1<<20)
	rec := r.postTo(context.Background(), "/v1/messages/count_tokens", `{"model":"x","messages":[{"role":"user","content":"hello world"}]}`, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"input_tokens":3}` {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if len(r.up.Calls()) != 0 || len(r.rows.All()) != 0 {
		t.Fatal("the count made an upstream call or a row")
	}
	rec = r.postTo(context.Background(), "/v1/messages/count_tokens", `{not json`, nil)
	if rec.Code != 400 || gjson.Get(rec.Body.String(), "type").String() != "error" || gjson.Get(rec.Body.String(), "error.type").String() != "invalid_request_error" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestClaudeCLIWarmupOnMessagesIsAnsweredLocally(t *testing.T) {
	r := newChatRig(t, 1<<20)
	rec := r.postTo(context.Background(), "/v1/messages", `{"model":"nothing/here","max_tokens":5,"messages":[{"role":"user","content":"Warmup"}]}`,
		map[string]string{"User-Agent": "claude-cli/1.0"})
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "content.0.text").String() != "CLI Command Execution: Clear Terminal" || len(r.up.Calls()) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestEveryClientRouteAnswersAnErrorInItsOwnShape(t *testing.T) {
	r := newChatRig(t, 1<<20)
	want := map[string]string{
		"/v1/messages": "error.type", "/v1/responses": "error.code", "/v1beta/models/x:generateContent": "error.status", "/v1/api/chat": "error",
	}
	for path, field := range want {
		rec := r.postTo(context.Background(), path, `{not json`, nil)
		if rec.Code != 400 || gjson.Get(rec.Body.String(), field).String() == "" {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

type cacheFlag struct{}

func (cacheFlag) ResponseCacheEnabled(context.Context, string) (bool, error) { return true, nil }

func TestTheCacheHeaderRoundTripsThroughTheRoute(t *testing.T) {
	r := newChatRigWith(t, 1<<20, func(d *pipeline.Deps) {
		d.Settings = tests.Settings{JSON: `{"responseCache":{"enabled":true}}`}
		d.ResponseCache = &pipeline.ResponseCache{Cache: cache.New(tests.NewCache(), cache.WithoutShared()), Flags: cacheFlag{}}
	}, tests.Reply{ContentType: "application/json", Body: chatReply})

	body := `{"model":"` + chatModel + `","messages":[{"role":"user","content":"hi"}]}`
	for i, want := range []string{"miss", "hit"} {
		rec := r.post(context.Background(), body, map[string]string{"X-Rllm-Cache": " USE "})
		if rec.Code != 200 || rec.Header().Get("X-Rllm-Cache") != want {
			t.Fatalf("call %d: %d %v %s", i+1, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	if n := len(r.up.Calls()); n != 1 {
		t.Fatalf("%d upstream calls", n)
	}
	for _, header := range []string{"", "yes", "bypass", "none"} {
		rec := r.post(context.Background(), body, map[string]string{"X-Rllm-Cache": header})
		if got := rec.Header().Get("X-Rllm-Cache"); got != "" {
			t.Errorf("%q: the answer carries %q although no wish was made", header, got)
		}
	}
}
