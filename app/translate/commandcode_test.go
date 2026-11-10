package translate

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func commandCodeBody(t *testing.T, r *Request, c *Ctx) gjson.Result {
	t.Helper()
	out, err := CommandCodeCodec{}.WriteRequest(r, c)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	return gjson.ParseBytes(out)
}

func commandCodeCtx() *Ctx {
	c := NewCtx(FormatOpenAI, FormatCommandCode)
	c.Session = "sess-42"
	return c
}

func TestTheCommandCodeBodyIsAThreadWithParams(t *testing.T) {
	r := &Request{Model: "m", Messages: []Message{
		{Role: RoleSystem, Content: TextPart("be brief")},
		{Role: RoleSystem, Content: TextPart("and kind")},
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
	r := &Request{Model: "m", Messages: []Message{user("hi")}}
	first := commandCodeBody(t, r, commandCodeCtx()).Get("threadId").String()
	second := commandCodeBody(t, r, commandCodeCtx()).Get("threadId").String()
	if first != second {
		t.Fatalf("the same conversation got %q then %q", first, second)
	}
	other := NewCtx(FormatOpenAI, FormatCommandCode)
	other.Session = "sess-99"
	if third := commandCodeBody(t, r, other).Get("threadId").String(); third == first {
		t.Fatal("two conversations share a thread")
	}
}

func TestCommandCodeTurnsAndToolCallsMapToTheirBlocks(t *testing.T) {
	r := &Request{Model: "m", Messages: []Message{
		user("read the file"),
		{Role: RoleAssistant, Reasoning: "I should read it", ToolCalls: []ToolCall{{ID: "c1", Name: "read", Arguments: `{"path":"a.txt"}`}}},
		{Role: RoleTool, ToolCallID: "c1", Name: "read", Content: TextPart("contents")},
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
	r := &Request{Model: "m", Messages: []Message{user("hi")},
		Tools: []Tool{{Name: "read", Description: "read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"p":{"type":"string"}}}`)}}}
	g := commandCodeBody(t, r, commandCodeCtx())
	if got := g.Get("params.tools.0.name").String(); got != "read" {
		t.Errorf("tool name %q", got)
	}
	if got := g.Get("params.tools.0.input_schema.properties.p.type").String(); got != "string" {
		t.Errorf("the schema was not carried: %s", g.Get("params.tools.0").Raw)
	}
}

func TestCommandCodeReadsTheAISDKStream(t *testing.T) {
	r := CommandCodeCodec{}.NewStreamReader(NewCtx(FormatCommandCode, FormatOpenAI))
	var out []Chunk
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
	if out[0].Text != "Hel" || out[0].Role != RoleAssistant {
		t.Errorf("first chunk %+v", out[0])
	}
	if out[1].Reasoning != "thinking" {
		t.Errorf("reasoning %+v", out[1])
	}
	var args string
	var finish string
	var usage *Usage
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
	if finish != FinishToolCalls {
		t.Errorf("finish %q", finish)
	}
	if usage == nil || usage.Prompt != 7 || usage.Completion != 3 {
		t.Errorf("usage %+v", usage)
	}
}

func TestCommandCodeAWholeToolCallIsNotRepeatedAsFragments(t *testing.T) {
	r := CommandCodeCodec{}.NewStreamReader(NewCtx(FormatCommandCode, FormatOpenAI))
	var out []Chunk
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
	r := CommandCodeCodec{}.NewStreamReader(NewCtx(FormatCommandCode, FormatOpenAI))
	out := r.Read("", []byte(`{"type":"error","error":"boom"}`))
	if len(out) != 1 || out[0].Err == nil {
		t.Fatalf("%+v", out)
	}
}

func TestCommandCodeFlushClosesATurnTheUpstreamLeftOpen(t *testing.T) {
	r := CommandCodeCodec{}.NewStreamReader(NewCtx(FormatCommandCode, FormatOpenAI))
	r.Read("", []byte(`{"type":"text-delta","text":"hi"}`))
	out := r.Flush()
	if len(out) != 1 || out[0].FinishReason != FinishStop {
		t.Fatalf("%+v", out)
	}
	fresh := CommandCodeCodec{}.NewStreamReader(NewCtx(FormatCommandCode, FormatOpenAI))
	fresh.Read("", []byte(`{"type":"finish","finishReason":"stop"}`))
	if out := fresh.Flush(); len(out) != 0 {
		t.Fatalf("a finished turn closed twice: %+v", out)
	}
}
