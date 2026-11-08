package pipeline

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/prefetch"
	"rllmx/tests"
)

const respStream = "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"cm\"}}\n\n" +
	"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n" +
	"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"cm\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n"

func codexReply() tests.Reply { return tests.Reply{ContentType: "text/event-stream", Body: respStream} }

func TestAChatCallToCodexIsShapedForTheBackend(t *testing.T) {
	r := newRig(t, codexReply())
	r.conn("cdx", "k1")
	body := `{"model":"cdx/cm","temperature":0.3,"max_tokens":50,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}],"prompt_cache_key":"client-key"}`
	out, _ := r.do("/v1/chat/completions", body)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if call.Path != "/codex/responses" {
		t.Fatalf("path %s", call.Path)
	}
	b := call.Body
	if !gjson.GetBytes(b, "stream").Bool() || gjson.GetBytes(b, "store").Bool() || gjson.GetBytes(b, "temperature").Exists() || gjson.GetBytes(b, "max_output_tokens").Exists() {
		t.Fatalf("%s", b)
	}
	if gjson.GetBytes(b, "instructions").String() != "be brief" || gjson.GetBytes(b, "reasoning.effort").String() != "low" {
		t.Fatalf("%s", b)
	}
	key := gjson.GetBytes(b, "prompt_cache_key").String()
	if len(key) != 32 || key == "client-key" {
		t.Fatalf("prompt_cache_key = %q", key)
	}
}

func TestCodexSessionKeyIsStablePerConversationAndAccountOnly(t *testing.T) {
	r := newRig(t, codexReply())
	r.conn("cdx", "k1")
	same := `{"model":"cdx/cm","messages":[{"role":"user","content":"hi"}],"session_id":"conv-1"}`
	r.do("/v1/chat/completions", same)
	r.do("/v1/chat/completions", same)
	r.do("/v1/chat/completions", strings.Replace(same, "conv-1", "conv-2", 1))
	calls := r.up.Calls()
	k := func(i int) string { return gjson.GetBytes(calls[i].Body, "prompt_cache_key").String() }
	if k(0) != k(1) || k(0) == k(2) {
		t.Fatalf("keys %q %q %q", k(0), k(1), k(2))
	}
}

func TestACompactCallGoesToTheCompactPathAndIsForwardedAsSent(t *testing.T) {
	compacted := `{"id":"resp_c","object":"response","model":"cm","status":"completed","output":[{"type":"compaction","encrypted_content":"opaque"}],"usage":{"input_tokens":9,"output_tokens":3,"total_tokens":12}}`
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: compacted})
	r.conn("cdx", "k1")
	out, _ := r.do("/v1/responses/compact", `{"model":"cdx/cm","input":[{"type":"message","role":"user","content":"long history"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if call := r.up.Calls()[0]; call.Path != "/codex/responses/compact" {
		t.Fatalf("path %s", call.Path)
	}
	if string(out.Body) != compacted {
		t.Fatalf("the compaction items were rebuilt:\n%s", out.Body)
	}
	if row := r.last(); row.PromptTokens != 9 || row.CompletionTokens != 3 {
		t.Fatalf("usage row %d/%d", row.PromptTokens, row.CompletionTokens)
	}
}

func TestACompactCallToAnotherProviderIsAPlainResponsesCall(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	out, _ := r.do("/v1/responses/compact", `{"model":"`+nodeID+`/gpt-x","input":"hi"}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if strings.HasSuffix(r.up.Calls()[0].Path, "/compact") {
		t.Fatalf("path %s", r.up.Calls()[0].Path)
	}
}

func TestAnOrdinaryCallIsNeverSentToTheCompactPath(t *testing.T) {
	r := newRig(t, codexReply())
	r.conn("cdx", "k1")
	r.do("/v1/responses", `{"model":"cdx/cm","input":"hi"}`)
	if strings.HasSuffix(r.up.Calls()[0].Path, "/compact") {
		t.Fatalf("path %s", r.up.Calls()[0].Path)
	}
}

func TestALiteModelGetsItsToolsInTheInputAndTheHeader(t *testing.T) {
	r := newRig(t, codexReply())
	r.conn("cdx", "k1")
	body := `{"model":"cdx/cl","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`
	r.do("/v1/chat/completions", body)
	call := r.up.Calls()[0]
	if call.Header.Get("x-openai-internal-codex-responses-lite") != "true" {
		t.Fatalf("headers %v", call.Header)
	}
	if gjson.GetBytes(call.Body, "input.0.type").String() != "additional_tools" || gjson.GetBytes(call.Body, "tools").Type != gjson.Null {
		t.Fatalf("%s", call.Body)
	}
	r2 := newRig(t, codexReply())
	r2.conn("cdx", "k1")
	r2.do("/v1/chat/completions", strings.Replace(body, "cdx/cl", "cdx/cm", 1))
	if r2.up.Calls()[0].Header.Get("x-openai-internal-codex-responses-lite") != "" {
		t.Fatal("the Lite header went with a full model")
	}
}

func TestACapacityErrorRotatesToTheNextAccount(t *testing.T) {
	full := "event: error\ndata: {\"error\":{\"message\":\"Selected model is at capacity. Please try a different model.\"}}\n\n"
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Body: full}, codexReply())
	r.conn("cdx", "k1")
	r.conn("cdx", "k2")
	out, _ := r.do("/v1/chat/completions", `{"model":"cdx/cm","messages":[{"role":"user","content":"hi"}]}`)
	calls := r.up.Calls()
	if out.Status != 200 || len(calls) != 2 || auth(calls[0]) == auth(calls[1]) {
		t.Fatalf("status %d after %d calls (%v)", out.Status, len(calls), out.Body)
	}
}

func TestACodexQuotaAnswerLocksTheAccountUntilItsReset(t *testing.T) {
	limit := tests.Reply{Status: 429, Body: `{"error":{"type":"usage_limit_reached","message":"limit","resets_in_seconds":3600}}`}
	r := newRig(t, limit, codexReply())
	r.conn("cdx", "k1")
	r.conn("cdx", "k2")
	body := `{"model":"cdx/cm","messages":[{"role":"user","content":"hi"}]}`
	if out, _ := r.do("/v1/chat/completions", body); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	r.do("/v1/chat/completions", body)
	calls := r.up.Calls()
	if len(calls) != 3 || auth(calls[2]) != auth(calls[1]) {
		t.Fatalf("the limited account was tried again: %d calls", len(calls))
	}
}

func TestACodexCallInlinesARemoteImage(t *testing.T) {
	img, hits := imageServer(t)
	r := newRig(t, codexReply())
	r.p.d.Prefetch = &prefetch.Fetcher{Client: img.Client()}
	r.conn("cdx", "k1")
	r.do("/v1/chat/completions", imageBody("cdx/cm-vision", img.URL+"/pic"))
	got := gjson.GetBytes(r.up.Calls()[0].Body, "input.0.content.1.image_url").String()
	if !strings.HasPrefix(got, "data:image/png;base64,") || hits.Load() != 1 {
		t.Fatalf("image_url = %q after %d fetches", got, hits.Load())
	}
}

func TestAGrokCallCarriesItsHeadersAndShapedBody(t *testing.T) {
	r := newRig(t, codexReply())
	r.accountConn("grk", "k1", `{"deviceId":"dev-1"}`)
	r.do("/v1/chat/completions", `{"model":"grk/gm","temperature":0.4,"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}],"prompt_cache_key":"client"}`)
	call := r.up.Calls()[0]
	if call.Path != "/grok/responses" || call.Header.Get("X-Grok-Client-Identifier") != "grok-shell" {
		t.Fatalf("%s %v", call.Path, call.Header)
	}
	h := call.Header
	if h.Get("X-Grok-Session-Id") == "" || h.Get("X-Grok-Session-Id") != h.Get("X-Grok-Conv-Id") || h.Get("X-Grok-Agent-Id") != "dev-1" || h.Get("X-Grok-Model-Override") != "gm" {
		t.Fatalf("%v", h)
	}
	if gjson.GetBytes(call.Body, "prompt_cache_key").String() != h.Get("X-Grok-Session-Id") || gjson.GetBytes(call.Body, "reasoning.effort").String() != "high" {
		t.Fatalf("%s", call.Body)
	}
	r2 := newRig(t, codexReply())
	r2.conn("grk", "k1")
	r2.do("/v1/chat/completions", `{"model":"grk/gn","messages":[{"role":"user","content":"hi"}]}`)
	if gjson.GetBytes(r2.up.Calls()[0].Body, "reasoning.effort").Exists() {
		t.Fatalf("effort sent to a model with no levels: %s", r2.up.Calls()[0].Body)
	}
}

func TestAGrokTurnDoesNotAdvanceWhenTheSameRequestMovesToAnotherAccount(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"slow"}}`}, codexReply())
	r.conn("grk", "k1")
	r.conn("grk", "k2")
	r.do("/v1/chat/completions", `{"model":"grk/gm","messages":[{"role":"user","content":"hi"}],"session_id":"s"}`)
	calls := r.up.Calls()
	if len(calls) < 2 || calls[0].Header.Get("X-Grok-Turn-Idx") != "1" || calls[1].Header.Get("X-Grok-Turn-Idx") != "1" {
		t.Fatalf("turn indexes %v", calls)
	}
}
