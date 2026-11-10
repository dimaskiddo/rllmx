package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// kiroBody writes a request in the Kiro format and parses what it produced.
func kiroBody(t *testing.T, r *Request, ctx *Ctx) gjson.Result {
	t.Helper()
	out, err := KiroCodec{}.WriteRequest(r, ctx)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !json.Valid(out) {
		t.Fatalf("invalid JSON: %s", out)
	}
	return gjson.ParseBytes(out)
}

func kiroCtx(model string) *Ctx {
	c := NewCtx(FormatOpenAI, FormatKiro)
	c.Model = model
	c.Session = "sess-1"
	return c
}

func TestTheKiroBodyIsAConversationState(t *testing.T) {
	r := &Request{Model: "claude-sonnet-4.5", Messages: []Message{
		{Role: RoleSystem, Content: TextPart("be brief")},
		user("hello"),
	}}
	g := kiroBody(t, r, kiroCtx("claude-sonnet-4.5"))

	if got := g.Get("conversationState.chatTriggerType").String(); got != "MANUAL" {
		t.Errorf("trigger %q", got)
	}
	if got := g.Get("conversationState.conversationId").String(); got != "sess-1" {
		t.Errorf("conversation id %q", got)
	}
	cur := g.Get("conversationState.currentMessage.userInputMessage")
	if cur.Get("origin").String() != "AI_EDITOR" || cur.Get("modelId").String() != "claude-sonnet-4.5" {
		t.Errorf("current %s", cur.Raw)
	}
	content := cur.Get("content").String()
	if !strings.Contains(content, "be brief") || !strings.Contains(content, "hello") {
		t.Errorf("the system text is not on the first user turn: %q", content)
	}
	if strings.Contains(g.Raw, `"systemPrompt"`) {
		t.Error("a top-level system field was sent, which the upstream refuses")
	}
}

func TestTheKiroBodyCarriesTheToolSpecsOnTheLastTurn(t *testing.T) {
	r := &Request{
		Model:    "m",
		Messages: []Message{user("weather?")},
		Tools:    []Tool{{Name: "get_weather", Description: "d", Parameters: json.RawMessage(`{"type":"object","properties":{"c":{"type":"string"}}}`)}},
	}
	g := kiroBody(t, r, kiroCtx("m"))
	specs := g.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.tools")
	if len(specs.Array()) != 1 {
		t.Fatalf("specs %s", specs.Raw)
	}
	if !strings.Contains(specs.Raw, `"name":"get_weather"`) || !strings.Contains(specs.Raw, `"json"`) {
		t.Errorf("spec %s", specs.Raw)
	}
	if g.Get("conversationState.history").Exists() && len(g.Get("conversationState.history").Array()) != 0 {
		t.Errorf("history %s", g.Get("conversationState.history").Raw)
	}
}

func TestAToolResultBecomesTheUserTurnThatAnswersTheCall(t *testing.T) {
	r := &Request{Model: "m",
		Messages: []Message{
			user("weather?"),
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
			{Role: RoleTool, ToolCallID: "c1", Content: TextPart("sunny")},
		},
		Tools: []Tool{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)}},
	}
	g := kiroBody(t, r, kiroCtx("m"))
	hist := g.Get("conversationState.history").Array()
	if len(hist) < 2 {
		t.Fatalf("history %s", g.Get("conversationState.history").Raw)
	}
	asst := hist[1].Get("assistantResponseMessage")
	if len(asst.Get("toolUses").Array()) != 1 {
		t.Fatalf("tool uses %s", asst.Raw)
	}
	// The result turn is the one the model answers, so it is the current
	// message rather than history.
	result := g.Get("conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0")
	if result.Get("status").String() != "success" || result.Get("content.0.text").String() != "sunny" {
		t.Errorf("result %s", result.Raw)
	}
}

func TestACallWithNoResultIsFlattenedToText(t *testing.T) {
	r := &Request{Model: "m",
		Messages: []Message{
			user("weather?"),
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "get_weather", Arguments: `{"city":"Paris"}`}}},
			user("never mind"),
		},
		Tools: []Tool{{Name: "get_weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}
	g := kiroBody(t, r, kiroCtx("m"))
	if strings.Contains(g.Raw, `"toolUses"`) {
		t.Errorf("an unanswered call was kept structured: %s", g.Raw)
	}
	if !strings.Contains(g.Raw, "[Tool call:") {
		t.Errorf("the call vanished: %s", g.Raw)
	}
}

func TestTheThinkingTagRidesOnTheFirstUserTurn(t *testing.T) {
	r := &Request{Model: "claude-sonnet-4.5-thinking", Messages: []Message{user("hi")}}
	g := kiroBody(t, r, kiroCtx("m"))
	content := g.Get("conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "<thinking_mode>enabled</thinking_mode>") {
		t.Errorf("no thinking trigger: %q", content)
	}
	if !strings.Contains(content, "<max_thinking_length>") {
		t.Errorf("no budget: %q", content)
	}
	if got := g.Get("conversationState.currentMessage.userInputMessage.modelId").String(); got != "claude-sonnet-4.5" {
		t.Errorf("the suffix reached the model id: %q", got)
	}
}

func TestAnAgenticModelGetsTheChunkedWritePrompt(t *testing.T) {
	r := &Request{Model: "claude-opus-4.5-agentic", Messages: []Message{user("write a file")}}
	g := kiroBody(t, r, kiroCtx("m"))
	content := g.Get("conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "CHUNKED WRITE PROTOCOL") {
		t.Errorf("no agentic prompt: %q", content)
	}
}

// kiroRead feeds events to a fresh reader and folds what it produced.
func kiroRead(t *testing.T, events ...[2]string) []Chunk {
	t.Helper()
	r := KiroCodec{}.NewStreamReader(NewCtx(FormatKiro, FormatOpenAI))
	var out []Chunk
	for _, ev := range events {
		out = append(out, r.Read(ev[0], []byte(ev[1]))...)
	}
	return append(out, r.Flush()...)
}

func TestKiroEventsBecomeChunks(t *testing.T) {
	got := kiroRead(t,
		[2]string{"assistantResponseEvent", `{"content":"hi "}`},
		[2]string{"reasoningContentEvent", `{"text":"think"}`},
		[2]string{"codeEvent", `{"content":"x := 1"}`},
		[2]string{"messageStopEvent", `{"stopReason":"end_turn"}`},
	)
	g := fold(got)
	if !strings.Contains(g.text, "hi ") || !strings.Contains(g.text, "x := 1") {
		t.Errorf("text %q", g.text)
	}
	if g.reasoning != "think" {
		t.Errorf("reasoning %q", g.reasoning)
	}
	if g.finish != FinishStop {
		t.Errorf("finish %q", g.finish)
	}
}

func TestKiroToolFragmentsAreJoinedAndReleasedAtTheEnd(t *testing.T) {
	got := kiroRead(t,
		[2]string{"toolUseEvent", `{"toolUseEvent":{"toolUseId":"c1","name":"get_weather","input":"{\"city\":"}}`},
		[2]string{"toolUseEvent", `{"toolUseEvent":{"toolUseId":"c1","input":"\"Paris\"}"}}`},
		[2]string{"messageStopEvent", `{}`},
		[2]string{"metadataEvent", `{"stopReason":"tool_use"}`},
	)
	g := fold(got)
	if g.name != "get_weather" || g.id != "c1" {
		t.Errorf("call %+v", g)
	}
	if g.args != `{"city":"Paris"}` {
		t.Errorf("arguments %q", g.args)
	}
	if g.finish != FinishToolCalls {
		t.Errorf("finish %q", g.finish)
	}
}

func TestAThinkingBlockSplitAcrossEventsBecomesReasoning(t *testing.T) {
	got := kiroRead(t,
		[2]string{"assistantResponseEvent", `{"content":"<thinking>step "}`},
		[2]string{"assistantResponseEvent", `{"content":"one</thinking>answer"}`},
	)
	g := fold(got)
	if g.reasoning != "step one" {
		t.Errorf("reasoning %q", g.reasoning)
	}
	if g.text != "answer" {
		t.Errorf("text %q", g.text)
	}
}

func TestKiroMetricsGiveTheTokenCounts(t *testing.T) {
	got := kiroRead(t,
		[2]string{"assistantResponseEvent", `{"content":"hi"}`},
		[2]string{"metricsEvent", `{"inputTokens":10,"outputTokens":4,"cacheReadInputTokens":2}`},
		[2]string{"messageStopEvent", `{"stopReason":"end_turn"}`},
	)
	g := fold(got)
	if g.usage.Prompt != 10 || g.usage.Completion != 4 || g.usage.Cached != 2 {
		t.Errorf("usage %+v", g.usage)
	}
}

func TestKiroToolNamesComeBackAsTheClientNamedThem(t *testing.T) {
	ctx := NewCtx(FormatKiro, FormatOpenAI)
	ctx.ToolName = func(n string) string { return strings.ReplaceAll(n, "_", "__") }
	r := KiroCodec{}.NewStreamReader(ctx)
	_ = r.Read("toolUseEvent", []byte(`{"toolUseEvent":{"toolUseId":"c1","name":"get_weather","input":{}}}`))
	got := r.Flush()
	if len(got) != 1 || got[0].ToolCalls[0].Name != "get__weather" {
		t.Errorf("call %+v", got)
	}
}

func TestAnUnknownKiroEventIsSkipped(t *testing.T) {
	if got := kiroRead(t, [2]string{"somethingNewEvent", `{"a":1}`}); len(got) != 0 {
		t.Errorf("unknown event produced %+v", got)
	}
}
