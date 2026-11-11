package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/tests"
)

// kimchiDecode reads a body as a map for a test.
func kimchiDecode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	return m
}

func TestKimchiBodyDropsWhatTheGatewayRefuses(t *testing.T) {
	body := []byte(`{
		"model": "kimi-k2.7",
		"system": [{"type": "text", "text": "be brief"}],
		"anthropic_version": "2023-06-01",
		"thinking": {"type": "enabled"},
		"top_k": 5,
		"messages": [
			{"role": "user", "content": "first"},
			{"role": "assistant", "content": [{"type": "text", "text": "hi", "cache_control": {"type": "ephemeral"}, "signature": "s"}],
			 "reasoning_content": "a long thought that a gateway does not want"},
			{"role": "user", "content": "second"}
		],
		"tools": [{"type": "function", "function": {"name": "read"}, "cache_control": {"type": "ephemeral"}}]
	}`)
	got := kimchiDecode(t, kimchiBody(body, "kimi-k2.7"))
	for _, k := range []string{"system", "anthropic_version", "thinking", "top_k"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s survived the clean", k)
		}
	}
	if got["messages"].([]any)[0].(map[string]any)["content"] != "be brief\n\nfirst" {
		t.Errorf("the system text was not folded into the first turn: %v", got["messages"])
	}
	assistant := got["messages"].([]any)[1].(map[string]any)
	if _, ok := assistant["reasoning_content"]; ok {
		t.Error("the long reasoning text survived")
	}
	part := assistant["content"].([]any)[0].(map[string]any)
	if _, ok := part["cache_control"]; ok {
		t.Error("the part kept its cache marker")
	}
	if _, ok := part["signature"]; ok {
		t.Error("the part kept its signature")
	}
	if _, ok := got["tools"].([]any)[0].(map[string]any)["cache_control"]; ok {
		t.Error("the tool kept its cache marker")
	}
}

func TestKimchiBodyKeepsTheReasoningFieldsOfAnotherBackend(t *testing.T) {
	body := []byte(`{"model":"kimi-k2.7","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`)
	got := kimchiDecode(t, kimchiBody(body, "kimi-k2.7"))
	if got["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want high", got["reasoning_effort"])
	}
}

func TestKimchiBodyDropsTheReasoningFieldsOfAnAnthropicBackend(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","reasoning_effort":"high","reasoning":{"effort":"high"},
		"messages":[{"role":"assistant","content":"x","reasoning_content":" "}]}`)
	got := kimchiDecode(t, kimchiBody(body, "claude-sonnet-4-6"))
	for _, k := range []string{"reasoning_effort", "reasoning"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s survived an Anthropic-backed model", k)
		}
	}
	// A short placeholder stays: the gateway's backend asks for one.
	if _, ok := got["messages"].([]any)[0].(map[string]any)["reasoning_content"]; !ok {
		t.Error("the placeholder was removed")
	}
}

func TestKimchiAnthropicBackendMatchesTheFamily(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"claude-sonnet-4-6", true},
		{"anthropic/claude-3", true},
		{"vendor-claude-x", true},
		{"kimi-k2.7", false},
		{"claudes", false},
	} {
		if got := kimchiAnthropic.MatchString(tc.model); got != tc.want {
			t.Errorf("%q = %v, want %v", tc.model, got, tc.want)
		}
	}
}

func TestKimchiSendsTheCleanedBody(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{"choices":[{"message":{"content":"ok"}}]}`})
	k := &Kimchi{Default: &executor.Default{Client: up.Client()}}
	req := executor.Request{
		URL: up.URL + "/openai/v1/chat/completions", Key: "key",
		Body: []byte(`{"model":"m","system":"s","top_k":3,"messages":[{"role":"user","content":"hi"}]}`),
	}
	resp, err := k.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	_ = resp.Body.Close()
	calls := up.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if bytes.Contains(calls[0].Body, []byte("top_k")) {
		t.Fatalf("the body was not cleaned: %s", calls[0].Body)
	}
	if !bytes.Contains(calls[0].Body, []byte("s\\n\\nhi")) {
		t.Fatalf("the system text was not folded: %s", calls[0].Body)
	}
}
