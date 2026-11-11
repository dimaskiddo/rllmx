package commandcode

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/translate"
)

func commandCodeBody(t *testing.T, r *translate.Request, c *translate.Ctx) gjson.Result {
	t.Helper()
	out, err := Codec{}.WriteRequest(r, c)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return gjson.ParseBytes(out)
}

func commandCodeCtx() *translate.Ctx {
	c := translate.NewCtx(translate.FormatOpenAI, translate.FormatCommandCode)
	c.Session = "sess-42"
	return c
}

func TestTheCommandCodeBodyIsAThreadWithParams(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart("be brief")},
		{Role: translate.RoleSystem, Content: translate.TextPart("and kind")},
		user("hello"),
	}}
	g := commandCodeBody(t, r, commandCodeCtx())
	if got := g.Get("params.system").String(); got != "be brief\n\nand kind" {
		t.Errorf("system %q", got)
	}
	if got := g.Get("params.messages.#").Int(); got != 1 {
		t.Errorf("%d messages, want the system turns out: %s", got, g.Get("params.messages").Raw)
	}
	if got := g.Get("params.messages.0.content.0.type").String(); got != "text" {
		t.Errorf("the content is not a block list: %s", g.Get("params.messages.0.content").Raw)
	}
	if got := g.Get("config.workingDir").String(); got != "/workspace" {
		t.Errorf("working dir %q", got)
	}
	if got := g.Get("config.environment").String(); got != "linux" {
		t.Errorf("environment %q", got)
	}
	if got := g.Get("threadId").String(); len(got) != 36 {
		t.Errorf("thread id %q", got)
	}
}

func TestTheCommandCodeThreadIsStablePerConversation(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{user("hi")}}
	first := commandCodeBody(t, r, commandCodeCtx()).Get("threadId").String()
	second := commandCodeBody(t, r, commandCodeCtx()).Get("threadId").String()
	if first != second {
		t.Fatalf("the same conversation got %q then %q", first, second)
	}
	other := translate.NewCtx(translate.FormatOpenAI, translate.FormatCommandCode)
	other.Session = "sess-99"
	if third := commandCodeBody(t, r, other).Get("threadId").String(); third == first {
		t.Fatal("two conversations share a thread")
	}
}

func TestCommandCodeTurnsAndToolCallsMapToTheirBlocks(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{
		user("read the file"),
		{Role: translate.RoleAssistant, Reasoning: "I should read it", ToolCalls: []translate.ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"a.txt"}`}}},
		{Role: translate.RoleTool, ToolCallID: "c1", Name: "read", Content: translate.TextPart("contents")},
	}}
	g := commandCodeBody(t, r, commandCodeCtx())
	if got := g.Get("params.messages.1.content.0.type").String(); got != "reasoning" {
		t.Errorf("an assistant turn with a call did not open with reasoning: %s", g.Get("params.messages.1.content").Raw)
	}
	if got := g.Get("params.messages.1.content.1.type").String(); got != "tool-call" {
		t.Errorf("no tool-call block: %s", g.Get("params.messages.1.content").Raw)
	}
	if got := g.Get("params.messages.1.content.1.input.path").String(); got != "a.txt" {
		t.Errorf("the arguments are not an object: %s", g.Get("params.messages.1.content.1.input").Raw)
	}
	if got := g.Get("params.messages.2.role").String(); got != "tool" {
		t.Errorf("a tool result has role %q", got)
	}
	if got := g.Get("params.messages.2.content.0.type").String(); got != "tool-result" {
		t.Errorf("no tool-result block: %s", g.Get("params.messages.2.content").Raw)
	}
	if got := g.Get("params.messages.2.content.0.output.value").String(); got != "contents" {
		t.Errorf("the result text is %q", got)
	}
}

func TestCommandCodeSendsOnlyTheToolsItsShapeTakes(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{user("hi")},
		Tools: []translate.Tool{{Name: "read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"p":{"type":"string"}}}`)}}}
	g := commandCodeBody(t, r, commandCodeCtx())
	if got := g.Get("params.tools.0.name").String(); got != "read" {
		t.Errorf("tool name %q", got)
	}
	if got := g.Get("params.tools.0.input_schema.properties.p.type").String(); got != "string" {
		t.Errorf("the schema was not carried: %s", g.Get("params.tools.0").Raw)
	}
}

func TestCommandCodeReadsTheAISDKStream(t *testing.T) {
	r := Codec{}.NewStreamReader(translate.NewCtx(translate.FormatCommandCode, translate.FormatOpenAI))
	var out []translate.Chunk
	for _, line := range []string{
		`{"type":"text-delta","text":"Hel"}`,
		`{"type":"reasoning-delta","text":"thinking"}`,
		`{"type":"tool-input-start","id":"t1","toolName":"read"}`,
		`{"type":"tool-input-delta","id":"t1","delta":"{\"p\":"}`,
		`{"type":"tool-input-delta","id":"t1","delta":"\"a\"}"}`,
		`{"type":"finish-step","finishReason":"tool-calls","usage":{"inputTokens":7,"outputTokens":3}}`,
		`{"type":"finish"}`,
	} {
		out = append(out, r.Read("", []byte(line))...)
	}
	if out[0].Text != "Hel" || out[0].Role != translate.RoleAssistant {
		t.Errorf("first chunk %+v", out[0])
	}
	if out[1].Reasoning != "thinking" {
		t.Errorf("reasoning %+v", out[1])
	}
	var args string
	var finish string
	var usage *translate.Usage
	for _, c := range out {
		for _, tc := range c.ToolCalls {
			args += tc.Arguments
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Usage != nil {
			usage = c.Usage
		}
	}
	if args != `{"p":"a"}` {
		t.Errorf("arguments %q", args)
	}
	if finish != translate.FinishToolCalls {
		t.Errorf("finish %q", finish)
	}
	if usage == nil || usage.Prompt != 7 || usage.Completion != 3 {
		t.Errorf("usage %+v", usage)
	}
}

func TestCommandCodeAWholeToolCallIsNotRepeatedAsFragments(t *testing.T) {
	r := Codec{}.NewStreamReader(translate.NewCtx(translate.FormatCommandCode, translate.FormatOpenAI))
	var out []translate.Chunk
	for _, line := range []string{
		`{"type":"tool-input-start","id":"t1","toolName":"read"}`,
		`{"type":"tool-input-delta","id":"t1","delta":"{}"}`,
		`{"type":"tool-call","toolCallId":"t1","toolName":"read","input":{"p":"a"}}`,
		`{"type":"tool-call","toolCallId":"t2","toolName":"write","input":{"p":"b"}}`,
	} {
		out = append(out, r.Read("", []byte(line))...)
	}
	var calls int
	var names []string
	for _, c := range out {
		for _, tc := range c.ToolCalls {
			if tc.Name != "" {
				names = append(names, tc.Name)
			}
			calls++
		}
	}
	if len(names) != 2 || names[0] != "read" || names[1] != "write" {
		t.Errorf("calls %v", names)
	}
}

func TestCommandCodeAnErrorEventIsAChunkError(t *testing.T) {
	r := Codec{}.NewStreamReader(translate.NewCtx(translate.FormatCommandCode, translate.FormatOpenAI))
	out := r.Read("", []byte(`{"type":"error","error":"boom"}`))
	if len(out) != 1 || out[0].Err == nil {
		t.Fatalf("%+v", out)
	}
}

func TestCommandCodeFlushClosesATurnTheUpstreamLeftOpen(t *testing.T) {
	r := Codec{}.NewStreamReader(translate.NewCtx(translate.FormatCommandCode, translate.FormatOpenAI))
	r.Read("", []byte(`{"type":"text-delta","text":"hi"}`))
	out := r.Flush()
	if len(out) != 1 || out[0].FinishReason != translate.FinishStop {
		t.Fatalf("%+v", out)
	}
	fresh := Codec{}.NewStreamReader(translate.NewCtx(translate.FormatCommandCode, translate.FormatOpenAI))
	fresh.Read("", []byte(`{"type":"finish","finishReason":"stop"}`))
	if out := fresh.Flush(); len(out) != 0 {
		t.Fatalf("a finished turn closed twice: %+v", out)
	}
}

func user(text string) translate.Message {
	return translate.Message{Role: translate.RoleUser, Content: translate.TextPart(text)}
}

func TestTheCommandCodeEnvelopeCarriesTheNewFields(t *testing.T) {
	g := commandCodeBody(t, &translate.Request{Model: "m", Messages: []translate.Message{user("hi")}}, commandCodeCtx())
	for _, path := range []string{"memory", "taste", "skills"} {
		if v := g.Get(path); v.Type != gjson.Null {
			t.Errorf("%s = %s, want null", path, v.Raw)
		}
	}
	if got := g.Get("permissionMode").String(); got != "standard" {
		t.Errorf("permissionMode %q", got)
	}
	if got := g.Get("params.max_tokens").Int(); got != 64000 {
		t.Errorf("max_tokens %d, want 64000", got)
	}
}

func TestACommandCodeToolResultNamesItsCallAndFlagsAnError(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{
		{Role: translate.RoleAssistant, ToolCalls: []translate.ToolCall{{ID: "c1", Name: "read", Arguments: `{}`}}},
		{Role: translate.RoleTool, ToolCallID: "c1", Content: translate.TextPart("boom"), Error: true},
		{Role: translate.RoleTool, ToolCallID: "gone", Content: translate.TextPart("x")},
	}}
	g := commandCodeBody(t, r, commandCodeCtx())
	first := g.Get("params.messages.1")
	if got := first.Get("content.0.toolName").String(); got != "read" {
		t.Errorf("tool name %q, want the call's", got)
	}
	if got := first.Get("content.0.output.type").String(); got != "error-text" {
		t.Errorf("output type %q, want error-text", got)
	}
	if got := g.Get("params.messages.2.content.0.toolName").String(); got != "unknown" {
		t.Errorf("a nameless result got %q, want unknown", got)
	}
}

func TestACommandCodeAssistantTurnKeepsNoPlaceholderReasoning(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{
		{Role: translate.RoleAssistant, ToolCalls: []translate.ToolCall{{ID: "c1", Name: "read", Arguments: `{"a":`}}},
	}}
	g := commandCodeBody(t, r, commandCodeCtx())
	blocks := g.Get("params.messages.0.content").Array()
	if len(blocks) == 0 || blocks[0].Get("type").String() == "reasoning" {
		t.Fatalf("a placeholder reasoning block was written: %s", g.Get("params.messages.0.content").Raw)
	}
	// The argument text is not JSON, so it rides as {raw: ...} instead of vanishing.
	if got := blocks[0].Get("input").Raw; !strings.Contains(got, `"raw"`) {
		t.Errorf("the unparseable arguments were dropped: %s", got)
	}
}

func TestTheCommandCodeReasoningEffortComesFromTheHub(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{user("hi")},
		Thinking: translate.Thinking{Mode: "level", Level: "high"}}
	if got := commandCodeBody(t, r, commandCodeCtx()).Get("params.reasoning_effort").String(); got != "high" {
		t.Errorf("effort %q", got)
	}
	r.Thinking = translate.Thinking{Mode: "budget", Budget: 4096}
	if v := commandCodeBody(t, r, commandCodeCtx()).Get("params.reasoning_effort"); v.Exists() {
		t.Errorf("a budget became an effort: %s", v.Raw)
	}
}

func TestCommandCodeJSONFollowsTheUpstreamShapes(t *testing.T) {
	cases := map[string]string{
		"":         `{}`,
		"  ":       `{}`,
		`{"a":1}`:  `{"a":1}`,
		`[1]`:      `[1]`,
		`"x"`:      `{"value":"x"}`,
		`5`:        `{"value":5}`,
		`not json`: `{"raw":"not json"}`,
	}
	for in, want := range cases {
		raw, _ := json.Marshal(commandCodeJSON(in))
		if string(raw) != want {
			t.Errorf("commandCodeJSON(%q) = %s, want %s", in, raw, want)
		}
	}
}
