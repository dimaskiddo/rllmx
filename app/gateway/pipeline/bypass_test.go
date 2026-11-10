package pipeline

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/tests"
)

const cliUA = "claude-cli/1.0 (external, cli)"

func openAIRequest(t *testing.T, body string) *translate.Request {
	t.Helper()
	codec, _ := translate.Lookup(translate.FormatOpenAI)
	req, err := codec.ReadRequest([]byte(body), translate.NewCtx(translate.FormatOpenAI, translate.FormatOpenAI))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func msgs(items ...string) string {
	return `{"model":"m","messages":[` + strings.Join(items, ",") + `]}`
}

func user(text string) string { return `{"role":"user","content":` + quote(text) + `}` }
func assistant(text string) string {
	return `{"role":"assistant","content":` + quote(text) + `}`
}
func system(text string) string { return `{"role":"system","content":` + quote(text) + `}` }

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

func TestMatchBypass(t *testing.T) {
	off := func() bool { return false }
	on := func() bool { return true }
	cases := []struct {
		name   string
		ua     string
		body   string
		naming func() bool
		want   string
		ok     bool
	}{
		{"warmup", cliUA, msgs(user("Warmup")), off, bypassText, true},
		{"count", cliUA, msgs(user("count")), off, bypassText, true},
		{"prefill", cliUA, msgs(user("go"), assistant("{")), off, bypassText, true},
		{"title", cliUA, msgs(user(titlePhrase + " hello")), off, bypassText, true},
		{"naming on", cliUA, msgs(system("decide isNewTopic"), user("fix the login bug now please")), on,
			`{"isNewTopic":true,"title":"fix the login"}`, true},
		{"naming off", cliUA, msgs(system("decide isNewTopic"), user("fix the login bug")), off, "", false},
		{"wrong agent", "curl/8", msgs(user("Warmup")), off, "", false},
		{"no agent", "", msgs(user("Warmup")), off, "", false},
		{"warmup is exact", cliUA, msgs(user("Warmup please")), off, "", false},
		{"count needs one message", cliUA, msgs(user("count"), user("count")), off, "", false},
		{"count must be a user message", cliUA, msgs(assistant("count")), off, "", false},
		{"prefill must be the last message", cliUA, msgs(assistant("{"), user("go")), off, "", false},
		{"plain chat", cliUA, msgs(user("write a function")), off, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := matchBypass(c.ua, openAIRequest(t, c.body), c.naming)
			if ok != c.ok || got != c.want {
				t.Fatalf("got %q %v, want %q %v", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestNamingIsNotReadForOrdinaryRequests(t *testing.T) {
	read := false
	matchBypass(cliUA, openAIRequest(t, msgs(user("hi"))), func() bool { read = true; return true })
	if read {
		t.Fatal("the setting must be read only for a topic probe")
	}
}

func TestMatchBypassNeedsMessages(t *testing.T) {
	if _, ok := matchBypass(cliUA, &translate.Request{}, func() bool { return true }); ok {
		t.Fatal("an empty request matched")
	}
}

func FuzzMatchBypass(f *testing.F) {
	for _, s := range []string{msgs(user("Warmup")), msgs(user("count")), msgs(assistant("{")), msgs(system("isNewTopic"), user("a b c d")), `{}`, `[]`} {
		f.Add([]byte(s), cliUA)
		f.Add([]byte(s), "curl")
	}
	codec, _ := translate.Lookup(translate.FormatOpenAI)
	f.Fuzz(func(t *testing.T, body []byte, ua string) {
		req, err := codec.ReadRequest(body, translate.NewCtx(translate.FormatOpenAI, translate.FormatOpenAI))
		if err != nil {
			return
		}
		_, ok := matchBypass(ua, req, func() bool { return true })
		if ok && !strings.Contains(ua, cliAgent) {
			t.Fatalf("matched without %s: %q", cliAgent, ua)
		}
	})
}

func (r *rig) probe(path, body, ua string) (Output, *httptest.ResponseRecorder) {
	r.t.Helper()
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	in := Input{Path: path, Body: []byte(body), Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-1", UserAgent: ua}
	before := len(r.rows.All())
	out := r.p.Handle(context.Background(), in, open)
	if n := len(r.rows.All()) - before; n != 1 {
		r.t.Errorf("a request left %d usage rows, want 1", n)
	}
	return out, rec
}

func TestBypassAnswersWithoutAnUpstreamOrAnAccount(t *testing.T) {
	r := newRig(t)
	out, _ := r.probe("/v1/chat/completions", `{"model":"nothing/here","messages":[{"role":"user","content":"Warmup"}]}`, cliUA)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != bypassText {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if gjson.GetBytes(out.Body, "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("%s", out.Body)
	}
	if n := len(r.up.Calls()); n != 0 {
		t.Fatalf("the upstream got %d calls", n)
	}
	row := r.last()
	if row.Status != 200 || row.Provider != "" || row.ConnectionID != "" || row.PromptTokens != 0 || row.CompletionTokens != 0 ||
		row.Stream || row.SourceFormat != "openai" || row.APIKeyID != "key-1" {
		t.Fatalf("%+v", row)
	}
}

func TestBypassWorksForAComboName(t *testing.T) {
	r := newRig(t)
	r.p.d.Resolver = &routing.Resolver{Registry: r.p.d.Registry, Snapshot: routing.NewSnapshot(source{combos: []string{"mycombo"}}, nil)}
	out, _ := r.probe("/v1/chat/completions", `{"model":"mycombo","messages":[{"role":"user","content":"count"}]}`, cliUA)
	if out.Status != 200 || len(r.up.Calls()) != 0 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestBypassIsOffWithoutTheCLIAgent(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	out, _ := r.probe("/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"Warmup"}]}`, "curl/8")
	if gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" || len(r.up.Calls()) != 1 {
		t.Fatalf("%s calls=%d", out.Body, len(r.up.Calls()))
	}
}

func TestBypassStreamsInTheClientsFormat(t *testing.T) {
	r := newRig(t)
	out, rec := r.probe("/v1/chat/completions", `{"model":"m","stream":true,"messages":[{"role":"user","content":"Warmup"}]}`, cliUA)
	if !out.Streamed || out.Status != 200 {
		t.Fatalf("%+v", out)
	}
	body := rec.Body.String()
	if !strings.Contains(body, bypassText) || !strings.HasSuffix(strings.TrimSpace(body), "data: [DONE]") || strings.Contains(body, "data: null") {
		t.Fatalf("%s", body)
	}
	if !r.last().Stream || len(r.up.Calls()) != 0 {
		t.Fatalf("%+v", r.last())
	}
}

func TestBypassInTheClaudeFormat(t *testing.T) {
	r := newRig(t)
	out, _ := r.probe("/v1/messages", `{"model":"m[1m]","max_tokens":10,"messages":[{"role":"user","content":"Warmup"}]}`, cliUA)
	if out.Status != 200 || gjson.GetBytes(out.Body, "content.0.text").String() != bypassText ||
		gjson.GetBytes(out.Body, "stop_reason").String() != "end_turn" || gjson.GetBytes(out.Body, "model").String() != "m" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}

	out, rec := r.probe("/v1/messages", `{"model":"m","stream":true,"max_tokens":10,"messages":[{"role":"user","content":"Warmup"}]}`, cliUA)
	body := rec.Body.String()
	if !out.Streamed || !strings.Contains(body, bypassText) || !strings.Contains(body, "message_stop") || strings.Contains(body, "[DONE]") {
		t.Fatalf("%+v %s", out, body)
	}
}

func TestBypassNamingFollowsTheSetting(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"system","content":"decide isNewTopic"},{"role":"user","content":"fix the login bug"}]}`
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")

	r.p.d.Settings = tests.Settings{JSON: `{"ccFilterNaming":false}`}
	out, _ := r.probe("/v1/chat/completions", body, cliUA)
	if len(r.up.Calls()) != 1 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("a setting that is off must send the request on: %s", out.Body)
	}

	r.p.d.Settings = tests.Settings{JSON: `{"ccFilterNaming":true}`}
	out, _ = r.probe("/v1/chat/completions", body, cliUA)
	got := gjson.GetBytes(out.Body, "choices.0.message.content").String()
	if len(r.up.Calls()) != 1 || gjson.Get(got, "title").String() != "fix the login" || !gjson.Get(got, "isNewTopic").Bool() {
		t.Fatalf("%q calls=%d", got, len(r.up.Calls()))
	}
}

func TestBypassIgnoresGeminiBodies(t *testing.T) {
	r := newRig(t)
	out, _ := r.probe("/v1beta/models/m:generateContent", `{"contents":[{"role":"user","parts":[{"text":"Warmup"}]}]}`, cliUA)
	if gjson.GetBytes(out.Body, "candidates.0.content.parts.0.text").String() == bypassText {
		t.Fatalf("a Gemini request must not be answered by the bypass: %s", out.Body)
	}
}
