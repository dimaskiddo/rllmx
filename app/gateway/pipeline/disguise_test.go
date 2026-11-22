package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/plan"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

func TestClientTool(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	cases := []struct {
		name string
		hdr  http.Header
		ua   string
		want string
	}{
		{"claude code agent", nil, "claude-cli/2.1.0 (external, cli)", ClientClaude},
		{"claude code lower", nil, "Claude-Code/1", ClientClaude},
		{"x-app cli", h("X-App", "cli"), "node", ClientClaude},
		{"x-app other", h("X-App", "web"), "node", ""},
		{"codex tui", nil, "codex-tui/1", ClientCodex},
		{"codex rust", nil, "codex_cli_rs/0.1", ClientCodex},
		{"codex desktop", nil, "Codex Desktop 3", ClientCodex},
		{"originator", h("Originator", "codex_vscode"), "x", ClientCodex},
		{"originator prefix only", h("Originator", "not_codex_"), "x", ""},
		{"gemini", nil, "gemini-cli/1", ClientGemini},
		{"copilot agent", nil, "GitHubCopilotChat/1", ClientCopilot},
		{"copilot intent", h("Openai-Intent", "conversation-panel"), "x", ClientCopilot},
		{"copilot initiator", h("X-Initiator", "user"), "x", ClientCopilot},
		{"deepseek", nil, "deepseek-tui", ClientDeepSeek},
		{"copilot wins over claude", h("X-Initiator", "user"), "claude-cli", ClientCopilot},
		{"curl", nil, "curl/8", ""},
		{"nothing", nil, "", ""},
		{"agent from the header when none given", h("User-Agent", "claude-cli/1"), "", ClientClaude},
	}
	for _, tc := range cases {
		if got := clientTool(tc.hdr, tc.ua); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOnlyTheHeadersThePipelineReadsAreKept(t *testing.T) {
	in := http.Header{}
	for _, k := range []string{"Authorization", "Cookie", "X-Forwarded-For", "X-Api-Key", "Content-Length"} {
		in.Set(k, "secret")
	}
	in.Set("User-Agent", "ua")
	in.Set("Anthropic-Beta", "a,b")
	in.Add("Anthropic-Beta", "c")
	in.Set("X-9router-Auto-Plan", "off")
	out := PickHeaders(in)
	if len(out) != 3 || out.Get("User-Agent") != "ua" || len(out.Values("Anthropic-Beta")) != 2 || out.Get("X-9router-Auto-Plan") != "off" {
		t.Fatalf("%v", out)
	}
}

func TestTheBetaListIsBuiltFromTheProvidersFlagsAndTheClientsAreMergedOnce(t *testing.T) {
	b := &upstream.Beta{Base: []string{"a", "b", "sum"}, Heavy: []string{"h1", "h2"}, HeavyPrefixes: []string{"claude-opus", "claude-sonnet"}, Summary: "sum"}
	plain := []byte(`{}`)
	cases := []struct {
		name   string
		model  string
		body   []byte
		client []string
		want   string
	}{
		{"base only", "claude-haiku-4", plain, nil, "a,b,sum"},
		{"opus gets the heavy flags", "claude-opus-5", plain, nil, "a,b,sum,h1,h2"},
		{"sonnet too", "claude-sonnet-5", plain, nil, "a,b,sum,h1,h2"},
		{"a model that only contains the word", "my-claude-opus", plain, nil, "a,b,sum"},
		{"summaries drop the summary flag", "claude-haiku-4", []byte(`{"thinking":{"display":"summarized"}}`), nil, "a,b"},
		{"other thinking display keeps it", "claude-haiku-4", []byte(`{"thinking":{"display":"omitted"}}`), nil, "a,b,sum"},
		{"client flags merge once in order", "claude-haiku-4", plain, []string{"x", "a", "y", "x"}, "a,b,sum,x,y"},
	}
	for _, tc := range cases {
		if got := betaList(b, tc.model, tc.body, tc.client); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if got := betaList(nil, "m", plain, []string{"x", "x"}); got != "x" {
		t.Errorf("no provider list: %q", got)
	}
}

func TestClientBetaFlagsAreCheckedAndBounded(t *testing.T) {
	h := http.Header{}
	h.Add("Anthropic-Beta", "good-flag, other.flag ,BAD,has space,with\r\nnewline,x\x00y")
	got := clientFlags(h)
	if len(got) != 2 || got[0] != "good-flag" || got[1] != "other.flag" {
		t.Fatalf("%q", got)
	}
	long := http.Header{}
	long.Set("Anthropic-Beta", strings.Repeat("a,", 1100))
	if len(clientFlags(long)) != 0 {
		t.Fatal("a header over 2 KiB was read")
	}
	many := http.Header{}
	var b strings.Builder
	for i := range 80 {
		b.WriteString("f")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(string(rune('a' + i/26)))
		b.WriteString(",")
	}
	many.Set("Anthropic-Beta", b.String())
	if n := len(clientFlags(many)); n != maxClientBeta {
		t.Fatalf("%d flags kept, want %d", n, maxClientBeta)
	}
}

const oauthToken = "sk-ant-oat01-secret-token"

func (r *rig) subCall(client http.Header, body string) (Output, *httptest.ResponseRecorder) {
	r.t.Helper()
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	in := Input{Path: "/v1/chat/completions", Body: []byte(body), Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-1", Header: client}
	return r.p.Handle(context.Background(), in, open), rec
}

func (r *rig) token(provider, email, token string, psd string) repository.Connection {
	r.t.Helper()
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: provider, AuthType: "oauth", Email: email, PSD: psd,
		Fields: map[string]string{"accessToken": `"` + token + `"`, "refreshToken": `"rt"`, "testStatus": `"active"`},
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

const toolsBody = `{"model":"sub/claude-x","messages":[` +
	`{"role":"user","content":"hi"},` +
	`{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},` +
	`{"role":"tool","tool_call_id":"t1","content":"ok"}],` +
	`"tools":[{"type":"function","function":{"name":"read_file","description":"d","parameters":{"type":"object","properties":{}}}}]}`

const claudeToolUse = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"tu_1","name":"read_file_ide","input":{"p":1}}],"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":2}}`

func TestARequestFromAnotherClientIsDisguisedAndTheToolNameComesBack(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeToolUse})
	r.token("sub", "a@x.test", oauthToken, "")
	out, _ := r.do2(toolsBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	body := call.Body
	if !strings.HasPrefix(gjson.GetBytes(body, "system.0.text").String(), "x-anthropic-billing-header:") {
		t.Fatalf("no billing block: %s", body)
	}
	if gjson.GetBytes(body, "metadata.user_id").String() == "" {
		t.Fatalf("no user id: %s", body)
	}
	if gjson.GetBytes(body, "tools.0.name").String() != "read_file_ide" || len(gjson.GetBytes(body, "tools").Array()) != 21 {
		t.Fatalf("tools: %s", gjson.GetBytes(body, "tools.#.name").Raw)
	}
	if gjson.GetBytes(body, "messages.1.content.#(type==\"tool_use\").name").String() != "read_file_ide" {
		t.Fatalf("history: %s", gjson.GetBytes(body, "messages").Raw)
	}
	if got := gjson.GetBytes(out.Body, "choices.0.message.tool_calls.0.function.name").String(); got != "read_file" {
		t.Fatalf("the client got %q, want read_file: %s", got, out.Body)
	}
}

func (r *rig) do2(body string) (Output, *httptest.ResponseRecorder) {
	r.t.Helper()
	return r.subCall(http.Header{"User-Agent": {"my-sdk/1"}}, body)
}

func TestTheHeadersOfAClaudeLogin(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	r.token("sub", "a@x.test", oauthToken, "")
	client := http.Header{"User-Agent": {"my-sdk/1"}, "Anthropic-Beta": {"client-flag, other"}, "Authorization": {"Bearer router-key"}, "X-Api-Key": {"nope"}}
	if out, _ := r.subCall(PickHeaders(client), subBody); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	want := map[string]string{
		"Authorization": "Bearer " + oauthToken, "User-Agent": "claude-cli/9.9.9 (external, sdk-cli)", "X-App": "cli",
		"Anthropic-Beta": "base-a,oauth-x,redact-y,client-flag,other",
	}
	for k, v := range want {
		if got := call.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if call.Header.Get("X-Api-Key") != "" {
		t.Error("the client's x-api-key reached the upstream")
	}
	if call.Query != "beta=true" {
		t.Errorf("query = %q", call.Query)
	}
	if sid := call.Header.Get("x-claude-code-session-id"); sid != gjson.Get(gjson.GetBytes(call.Body, "metadata.user_id").String(), "session_id").String() || sid == "" {
		t.Errorf("session header %q does not agree with the body's user id", sid)
	}
}

func TestHeavyBetaFlagsFollowTheModel(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	r.token("sub", "a@x.test", oauthToken, "")
	r.subCall(nil, `{"model":"sub/claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	r.subCall(nil, subBody)
	calls := r.up.Calls()
	if got := calls[0].Header.Get("Anthropic-Beta"); got != "base-a,oauth-x,redact-y,heavy-1" {
		t.Errorf("opus: %q", got)
	}
	if got := calls[1].Header.Get("Anthropic-Beta"); got != "base-a,oauth-x,redact-y" {
		t.Errorf("other: %q", got)
	}
}

func TestThinkingSummariesDropTheSummaryFlagAndOtherDisplaysKeepIt(t *testing.T) {
	for _, tc := range []struct {
		display string
		keeps   bool
	}{{"summarized", false}, {"omitted", true}, {"", true}} {
		r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
		r.token("sub", "a@x.test", oauthToken, "")
		thinking := `{"type":"enabled","budget_tokens":2000`
		if tc.display != "" {
			thinking += `,"display":"` + tc.display + `"`
		}
		thinking += `}`
		body := `{"model":"sub/claude-x","max_tokens":4000,"messages":[{"role":"user","content":"hi"}],"thinking":` + thinking + `}`
		out := r.p.Handle(context.Background(), Input{Path: "/v1/messages", Body: []byte(body), Caller: identity.Caller{ID: "key-1"}, RequestID: "r"},
			func() (stream.Client, error) { return stream.NewSSE(httptest.NewRecorder(), stream.Options{}) })
		if out.Status != 200 {
			t.Fatalf("%q: %d %s", tc.display, out.Status, out.Body)
		}
		call := r.up.Calls()[0]
		if got := strings.Contains(call.Header.Get("Anthropic-Beta"), "redact-y"); got != tc.keeps {
			t.Errorf("display %q: beta %q, redact flag kept = %v, want %v (body thinking %s)", tc.display, call.Header.Get("Anthropic-Beta"), got, tc.keeps, gjson.GetBytes(call.Body, "thinking").Raw)
		}
	}
}

func TestAClaudeCodeClientIsNeverDisguised(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeToolUse})
	r.token("sub", "a@x.test", oauthToken, "")
	out, _ := r.subCall(PickHeaders(http.Header{"User-Agent": {"claude-cli/2.1.0 (external, cli)"}}), toolsBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	body := r.up.Calls()[0].Body
	if gjson.GetBytes(body, "tools.0.name").String() != "read_file" || len(gjson.GetBytes(body, "tools").Array()) != 1 ||
		gjson.GetBytes(body, "system.0.text").String() != "" || gjson.GetBytes(body, "metadata").Exists() {
		t.Fatalf("a Claude Code client was disguised: %s", body)
	}
}

func TestAnAPIKeyIsNeverDisguisedAndKeepsItsHeaders(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "sub", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"sk-ant-oat01-looks-like-a-login"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	r.do2(toolsBody)
	call := r.up.Calls()[0]
	if gjson.GetBytes(call.Body, "tools.0.name").String() != "read_file" || gjson.GetBytes(call.Body, "system").Exists() {
		t.Fatalf("a key was disguised: %s", call.Body)
	}
	if call.Query != "" || call.Header.Get("Anthropic-Beta") != "" || call.Header.Get("X-App") != "" {
		t.Fatalf("a key got a login's address or headers: %q %v", call.Query, call.Header)
	}
}

func TestATokenThatIsNotAClaudeLoginTokenIsNotDisguised(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: claudeMsg})
	r.token("sub", "a@x.test", "some-other-token", "")
	r.do2(toolsBody)
	call := r.up.Calls()[0]
	if gjson.GetBytes(call.Body, "tools.0.name").String() != "read_file" {
		t.Fatalf("%s", call.Body)
	}
	if call.Header.Get("Authorization") != "Bearer some-other-token" || call.Header.Get("User-Agent") != "claude-cli/9.9.9 (external, sdk-cli)" {
		t.Fatalf("the headers still apply: %v", call.Header)
	}
}

func TestAnUncloakedAttemptNeverStripsASuffix(t *testing.T) {
	reply := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"tu_1","name":"real_ide","input":{}}],"stop_reason":"tool_use","usage":{"input_tokens":4,"output_tokens":2}}`
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: reply})
	r.token("sub", "a@x.test", "some-other-token", "")
	out, _ := r.do2(toolsBody)
	if got := gjson.GetBytes(out.Body, "choices.0.message.tool_calls.0.function.name").String(); got != "real_ide" {
		t.Fatalf("a name was changed on an attempt that renamed nothing: %q", got)
	}
}

const toolStream = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-x\",\"usage\":{\"input_tokens\":3}}}\n\n" +
	"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu_1\",\"name\":\"read_file_ide\"}}\n\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
	"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

func TestTheToolNameComesBackOnATranslatedStreamAndOnAPassthroughStream(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, want string
	}{
		{"translated to OpenAI", "/v1/chat/completions", strings.Replace(toolsBody, `"model":"sub/claude-x"`, `"model":"sub/claude-x","stream":true`, 1), `"name":"read_file"`},
		{"passthrough to a Claude client", "/v1/messages", `{"model":"sub/claude-x","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`, `"name":"read_file"`},
	} {
		r := newRig(t, tests.Reply{ContentType: "text/event-stream", Body: toolStream})
		r.token("sub", "a@x.test", oauthToken, "")
		rec := httptest.NewRecorder()
		open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
		in := Input{Path: tc.path, Body: []byte(tc.body), Caller: identity.Caller{ID: "key-1"}, RequestID: "r", Header: PickHeaders(http.Header{"User-Agent": {"sdk/1"}})}
		out := r.p.Handle(context.Background(), in, open)
		if !out.Streamed {
			t.Fatalf("%s: %d %s", tc.name, out.Status, out.Body)
		}
		got := rec.Body.String()
		if !strings.Contains(got, tc.want) || strings.Contains(got, "_ide") {
			t.Errorf("%s: the client saw the disguised name:\n%s", tc.name, got)
		}
		if gjson.GetBytes(r.up.Calls()[0].Body, "tools.0.name").String() != "read_file_ide" {
			t.Errorf("%s: the upstream was not disguised", tc.name)
		}
	}
}

func TestAPassthroughStreamIsForwardedUntouchedWhenNothingWasRenamed(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Body: toolStream})
	r.token("sub", "a@x.test", "some-other-token", "")
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	body := `{"model":"sub/claude-x","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read_file","input_schema":{"type":"object"}}]}`
	out := r.p.Handle(context.Background(), Input{Path: "/v1/messages", Body: []byte(body), Caller: identity.Caller{ID: "key-1"}, RequestID: "r"}, open)
	if !out.Streamed || !strings.Contains(rec.Body.String(), `"name":"read_file_ide"`) {
		t.Fatalf("an undisguised stream was rewritten:\n%s", rec.Body.String())
	}
}

func TestEachAttemptRebuildsTheDisguiseSoNoMapCrossesAccounts(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"rate limit"}}`}, tests.Reply{ContentType: "application/json", Body: claudeToolUse})
	r.token("sub", "a@x.test", oauthToken+"-1", "")
	r.token("sub", "b@x.test", oauthToken+"-2", "")
	out, _ := r.do2(toolsBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	calls := r.up.Calls()
	if len(calls) != 2 {
		t.Fatalf("%d calls", len(calls))
	}
	d1 := gjson.Get(gjson.GetBytes(calls[0].Body, "metadata.user_id").String(), "device_id").String()
	d2 := gjson.Get(gjson.GetBytes(calls[1].Body, "metadata.user_id").String(), "device_id").String()
	if d1 == "" || d1 == d2 {
		t.Fatalf("the two accounts must not share a device id: %q %q", d1, d2)
	}
	if got := gjson.GetBytes(out.Body, "choices.0.message.tool_calls.0.function.name").String(); got != "read_file" {
		t.Fatalf("restored name %q", got)
	}
}

func TestACodexLoginSendsItsIdentityAccountAndAKeyedSession(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.token("subx", "a@x.test", "codex-token", `{"chatgptAccountId":"acct-9"}`)
	client := PickHeaders(http.Header{"Session-Id": {"client-chosen-session"}})
	if out, _ := r.subCall(client, `{"model":"subx/x1","messages":[{"role":"user","content":"hi"}]}`); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	for k, v := range map[string]string{
		"Authorization": "Bearer codex-token", "Originator": "codex_cli_rs", "User-Agent": "codex_cli_rs/9.9.9",
		"Version": "9.9.9", "Chatgpt-Account-Id": "acct-9",
	} {
		if got := call.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	sid := call.Header.Get("Session_id")
	if sid == "" || sid == "client-chosen-session" || strings.Contains(sid, "client") {
		t.Fatalf("the session header is %q: a client's value must never reach the upstream as it is", sid)
	}
}

func TestTheCodexAccountHeaderTakesTheFirstFieldThatIsSet(t *testing.T) {
	for _, tc := range []struct{ psd, want string }{
		{`{"workspaceId":"w","chatgptAccountId":"c","accountId":"a"}`, "w"},
		{`{"chatgptAccountId":"c","accountId":"a"}`, "c"},
		{`{"accountId":"a"}`, "a"},
		{``, ""},
	} {
		r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		r.token("subx", "a@x.test", "t", tc.psd)
		r.subCall(nil, `{"model":"subx/x1","messages":[{"role":"user","content":"hi"}]}`)
		if got := r.up.Calls()[0].Header.Get("ChatGPT-Account-ID"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.psd, got, tc.want)
		}
	}
}

func TestTheSessionHeaderIsStablePerConversationAndAccount(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.token("subx", "a@x.test", "t", `{"accountId":"a"}`)
	body := `{"model":"subx/x1","messages":[{"role":"user","content":"hi"}]}`
	h1 := PickHeaders(http.Header{"Session-Id": {"conv-1"}})
	h2 := PickHeaders(http.Header{"Session-Id": {"conv-2"}})
	r.subCall(h1, body)
	r.subCall(h1, body)
	r.subCall(h2, body)
	calls := r.up.Calls()
	a, b, c := calls[0].Header.Get("Session_id"), calls[1].Header.Get("Session_id"), calls[2].Header.Get("Session_id")
	if a != b || a == c {
		t.Fatalf("session ids %q %q %q", a, b, c)
	}
}

func TestTheClientToolIsPartOfTheCacheKey(t *testing.T) {
	r := newCacheRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	a := r.use(chatBody)
	a.Header = PickHeaders(http.Header{"User-Agent": {"claude-cli/2"}})
	b := r.use(chatBody)
	b.Header = PickHeaders(http.Header{"User-Agent": {"curl/8"}})
	r.send(a)
	if out, _ := r.send(a); out.Header.Get(CacheHeader) != "hit" {
		t.Fatalf("the same client missed: %v", out.Header)
	}
	if out, _ := r.send(b); out.Header.Get(CacheHeader) != "miss" {
		t.Fatalf("another client shared the entry: %v", out.Header)
	}
}

func TestEveryBypassSwitchSurvivesPickHeaders(t *testing.T) {
	in := http.Header{}
	names := []string{saverBypassHeader, skill.BypassLocal, skill.BypassECC, plan.BypassHeader}
	for _, n := range names {
		in.Set(n, "off")
	}
	in.Set("Cookie", "session=1")
	got := PickHeaders(in)
	for _, n := range names {
		if got.Get(n) != "off" {
			t.Errorf("%s was dropped, so the client's bypass never reaches the pipeline", n)
		}
	}
	if got.Get("Cookie") != "" {
		t.Error("a header the pipeline does not read was kept")
	}
}
