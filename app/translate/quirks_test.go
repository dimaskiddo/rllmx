package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
)

func ctxWith(client, target, model string, tr upstream.Transport) *Ctx {
	c := NewCtx(client, target)
	c.Model = model
	c.Entry = upstream.Entry{Transport: tr}
	return c
}

func writeOpenAI(t *testing.T, in string, c *Ctx) string {
	t.Helper()
	req, err := ClaudeCodec{}.ReadRequest([]byte(in), c)
	if c.ClientFormat == FormatOpenAI {
		req, err = OpenAICodec{}.ReadRequest([]byte(in), c)
	}
	if err != nil {
		t.Fatal(err)
	}
	out, err := OpenAICodec{}.WriteRequest(req, c)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

const claudeCached = `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}},{"type":"text","text":"there"}]}]}`

func TestCacheMarkersReachAnOpenAIUpstreamOnlyWhenTheProviderAsksForThem(t *testing.T) {
	keep := ctxWith(FormatClaude, FormatOpenAI, "m", upstream.Transport{Quirks: upstream.Quirks{PreserveCacheControl: true}})
	out := writeOpenAI(t, claudeCached, keep)
	if got := gjson.Get(out, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("marker lost: %s", out)
	}
	if gjson.Get(out, "messages.0.content.1.cache_control").Exists() {
		t.Fatalf("a marker appeared on a block that had none: %s", out)
	}
	plain := ctxWith(FormatClaude, FormatOpenAI, "m", upstream.Transport{})
	out = writeOpenAI(t, claudeCached, plain)
	if strings.Contains(out, "cache_control") {
		t.Fatalf("a provider that did not ask got a marker: %s", out)
	}
	if gjson.Get(out, "messages.0.content").Type != gjson.String {
		t.Fatalf("a plain text message must stay a string: %s", out)
	}
}

func TestAMessageWithoutMarkersStaysAStringEvenWhenTheProviderKeepsThem(t *testing.T) {
	keep := ctxWith(FormatClaude, FormatOpenAI, "m", upstream.Transport{Quirks: upstream.Quirks{PreserveCacheControl: true}})
	out := writeOpenAI(t, `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, keep)
	if gjson.Get(out, "messages.0.content").Type != gjson.String {
		t.Fatalf("%s", out)
	}
}

const toolReq = `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"a","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}]}`

func TestTypelessClaudeToolsGetTypeCustomOnlyForProvidersThatRequireIt(t *testing.T) {
	need := ctxWith(FormatClaude, FormatClaude, "m", upstream.Transport{Quirks: upstream.Quirks{RequireClaudeToolType: true}})
	req, _ := ClaudeCodec{}.ReadRequest([]byte(toolReq), need)
	out, _ := ClaudeCodec{}.WriteRequest(req, need)
	if gjson.GetBytes(out, "tools.0.type").String() != "custom" {
		t.Fatalf("%s", gjson.GetBytes(out, "tools").Raw)
	}
	if gjson.GetBytes(out, "tools.1.type").String() != "web_search_20250305" {
		t.Fatalf("a typed tool changed: %s", gjson.GetBytes(out, "tools.1").Raw)
	}
	other := ctxWith(FormatClaude, FormatClaude, "m", upstream.Transport{})
	req, _ = ClaudeCodec{}.ReadRequest([]byte(toolReq), other)
	out, _ = ClaudeCodec{}.WriteRequest(req, other)
	if gjson.GetBytes(out, "tools.0.type").Exists() {
		t.Fatalf("a provider that does not require a type got one: %s", gjson.GetBytes(out, "tools.0").Raw)
	}
}

const history = `{"model":"%s","messages":[` +
	`{"role":"user","content":"q"},` +
	`{"role":"assistant","content":"plain answer"},` +
	`{"role":"user","content":"q2"},` +
	`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
	`{"role":"tool","tool_call_id":"c1","content":"ok"},` +
	`{"role":"assistant","content":"has thoughts","reasoning_content":"mine"}]}`

func reasoningOf(t *testing.T, model string, tr upstream.Transport) string {
	t.Helper()
	c := ctxWith(FormatOpenAI, FormatOpenAI, model, tr)
	return writeOpenAI(t, strings.Replace(history, "%s", model, 1), c)
}

func TestReasoningPlaceholderFollowsTheScope(t *testing.T) {
	all := reasoningOf(t, "any", upstream.Transport{ReasoningInject: upstream.ReasoningAll})
	if gjson.Get(all, "messages.1.reasoning_content").String() != " " || gjson.Get(all, "messages.3.reasoning_content").String() != " " {
		t.Fatalf("scope all: %s", all)
	}
	if gjson.Get(all, "messages.5.reasoning_content").String() != "mine" {
		t.Fatalf("existing reasoning was replaced: %s", all)
	}
	if gjson.Get(all, "messages.0.reasoning_content").Exists() || gjson.Get(all, "messages.4.reasoning_content").Exists() {
		t.Fatalf("a non-assistant message got one: %s", all)
	}
	tc := reasoningOf(t, "any", upstream.Transport{ReasoningInject: upstream.ReasoningToolCalls})
	if gjson.Get(tc, "messages.1.reasoning_content").Exists() || gjson.Get(tc, "messages.3.reasoning_content").String() != " " {
		t.Fatalf("scope toolCalls: %s", tc)
	}
	none := reasoningOf(t, "any", upstream.Transport{})
	if strings.Contains(none, `"reasoning_content":" "`) {
		t.Fatalf("no scope, yet injected: %s", none)
	}
}

func TestTheModelFamilyDecidesWhenTheProviderNamesNoScope(t *testing.T) {
	if got := reasoningOf(t, "deepseek-v4-pro", upstream.Transport{}); gjson.Get(got, "messages.1.reasoning_content").String() != " " {
		t.Fatalf("deepseek: %s", got)
	}
	if got := reasoningOf(t, "kimi-k2.5", upstream.Transport{}); gjson.Get(got, "messages.1.reasoning_content").Exists() || gjson.Get(got, "messages.3.reasoning_content").String() != " " {
		t.Fatalf("kimi: %s", got)
	}
	if got := reasoningOf(t, "deepseek-v4-pro", upstream.Transport{ReasoningInject: upstream.ReasoningToolCalls}); gjson.Get(got, "messages.1.reasoning_content").Exists() {
		t.Fatalf("the provider's scope must beat the model's: %s", got)
	}
}

const claudeCachedWithImage = `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":[` +
	`{"type":"text","text":"look","cache_control":{"type":"ephemeral"}},` +
	`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}`

func TestAMixedMessageKeepsItsMarkerOnlyForAProviderThatAsksForIt(t *testing.T) {
	keep := ctxWith(FormatClaude, FormatOpenAI, "m", upstream.Transport{Quirks: upstream.Quirks{PreserveCacheControl: true}})
	if out := writeOpenAI(t, claudeCachedWithImage, keep); gjson.Get(out, "messages.0.content.0.cache_control.type").String() != "ephemeral" {
		t.Fatalf("marker lost: %s", out)
	}
	plain := ctxWith(FormatClaude, FormatOpenAI, "m", upstream.Transport{})
	out := writeOpenAI(t, claudeCachedWithImage, plain)
	if strings.Contains(out, "cache_control") || gjson.Get(out, "messages.0.content.1.type").String() != "image_url" {
		t.Fatalf("a provider that did not ask got a marker, or the image went: %s", out)
	}
}
