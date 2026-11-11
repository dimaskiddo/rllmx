package responses

import (
	"encoding/json"
	"rllmx/app/llm/translate"
	"strings"
	"testing"
)

func rctx() *translate.Ctx {
	return translate.NewCtx(translate.FormatResponses, translate.FormatOpenAI)
}

func readResponses(t *testing.T, body string, c *translate.Ctx) *translate.Request {
	t.Helper()
	r, err := ResponsesCodec{}.ReadRequest([]byte(body), c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInstructionsBecomeTheSystemTurn(t *testing.T) {
	r := readResponses(t, `{"model":"m","instructions":"be brief","input":"hello"}`, rctx())
	if r.Messages[0].Role != translate.RoleSystem || r.Messages[0].Text() != "be brief" || r.Messages[1].Text() != "hello" {
		t.Fatalf("%+v", r.Messages)
	}
}

func TestAnEmptyInputBecomesAPlaceholderUserTurn(t *testing.T) {
	for _, body := range []string{`{"model":"m"}`, `{"model":"m","input":""}`, `{"model":"m","input":[]}`, `{"model":"m","input":"   "}`} {
		r := readResponses(t, body, rctx())
		if len(r.Messages) != 1 || r.Messages[0].Role != translate.RoleUser || r.Messages[0].Text() != "..." {
			t.Errorf("%s: %+v", body, r.Messages)
		}
	}
	if _, err := (ResponsesCodec{}).ReadRequest([]byte(`{"model":"m","input":5}`), rctx()); err == nil {
		t.Error("a numeric input was accepted")
	}
}

func TestItemsWithoutATypeButWithARoleAreMessages(t *testing.T) {
	r := readResponses(t, `{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, rctx())
	if len(r.Messages) != 1 || r.Messages[0].Text() != "hi" {
		t.Fatalf("%+v", r.Messages)
	}
}

func TestReasoningItemsAttachToTheNextAssistantTurn(t *testing.T) {
	body := `{"model":"m","input":[
	 {"type":"message","role":"user","content":"q"},
	 {"type":"reasoning","summary":[{"type":"summary_text","text":"step one"}],"encrypted_content":"ENC"},
	 {"type":"reasoning","summary":[{"type":"summary_text","text":"step two"}]},
	 {"type":"function_call","call_id":"c1","name":"f","arguments":"{\"a\":1}"},
	 {"type":"function_call_output","call_id":"c1","output":"ok"}]}`
	r := readResponses(t, body, rctx())
	if len(r.Messages) != 3 {
		t.Fatalf("%d messages: %+v", len(r.Messages), r.Messages)
	}
	a := r.Messages[1]
	if a.Role != translate.RoleAssistant || a.Reasoning != "step one\nstep two" || a.ReasoningSignature != "ENC" || len(a.ToolCalls) != 1 {
		t.Fatalf("%+v", a)
	}
	if r.Messages[2].Role != translate.RoleTool || r.Messages[2].ToolCallID != "c1" || r.Messages[2].Text() != "ok" {
		t.Fatalf("%+v", r.Messages[2])
	}
}

func TestReasoningBeforeAUserTurnIsDropped(t *testing.T) {
	r := readResponses(t, `{"model":"m","input":[{"type":"reasoning","summary":[{"text":"orphan"}]},{"type":"message","role":"user","content":"q"},{"type":"message","role":"assistant","content":"a"}]}`, rctx())
	if r.Messages[1].Reasoning != "" {
		t.Fatalf("an orphaned reasoning item reached a later assistant turn: %+v", r.Messages[1])
	}
}

func TestACallWithNoNameIsSkippedAndSeveralCallsShareOneTurn(t *testing.T) {
	r := readResponses(t, `{"model":"m","input":[{"type":"function_call","call_id":"a","name":"f","arguments":"{}"},{"type":"function_call","call_id":"b","name":"  ","arguments":"{}"},{"type":"function_call","call_id":"c","name":"g","arguments":{"x":1}}]}`, rctx())
	calls := r.Messages[0].ToolCalls
	if len(r.Messages) != 1 || len(calls) != 2 || calls[0].Name != "f" || calls[1].Name != "g" || calls[1].Arguments != `{"x":1}` {
		t.Fatalf("%+v", r.Messages)
	}
}

func TestCustomToolsRoundTripWithTheirNames(t *testing.T) {
	c := rctx()
	body := `{"model":"m","tools":[{"type":"custom","name":"apply_patch","description":"patch","format":{"syntax":"lark","definition":"start: x"}},{"type":"function","name":"f","parameters":{"type":"object"}}],
	 "input":[{"type":"custom_tool_call","call_id":"c","name":"apply_patch","input":"*** patch"}]}`
	r := readResponses(t, body, c)
	if !c.CustomTools["apply_patch"] || c.CustomTools["f"] {
		t.Fatalf("custom tools %v", c.CustomTools)
	}
	if r.Tools[0].Parameters == nil || !strings.Contains(string(r.Tools[0].Parameters), `"input"`) || !strings.Contains(r.Tools[0].Description, "start: x") {
		t.Errorf("a custom tool must reach a chat upstream as one string parameter: %+v", r.Tools[0])
	}
	call := r.Messages[0].ToolCalls[0]
	if !call.Custom || call.Arguments != `{"input":"*** patch"}` {
		t.Fatalf("%+v", call)
	}
	// and back: the answer names it a custom_tool_call again
	out, err := ResponsesCodec{}.WriteResponse(&translate.Response{ID: "r", Model: "m", Message: translate.Message{Role: translate.RoleAssistant, ToolCalls: []translate.ToolCall{{ID: "c2", Name: "apply_patch", Arguments: `{"input":"p"}`}}}}, c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	item := m["output"].([]any)[0].(map[string]any)
	if item["type"] != "custom_tool_call" || item["input"] != "p" || item["name"] != "apply_patch" {
		t.Fatalf("%v", item)
	}
}

func TestHostedToolsWithoutANameAreDropped(t *testing.T) {
	r := readResponses(t, `{"model":"m","input":"x","tools":[{"type":"request_user_input"},{"type":"function","name":"f","parameters":{"type":"object"}},{"type":"web_search"}]}`, rctx())
	if len(r.Tools) != 1 || r.Tools[0].Name != "f" {
		t.Fatalf("%+v", r.Tools)
	}
}

func TestAdditionalToolsItemsAddTools(t *testing.T) {
	r := readResponses(t, `{"model":"m","input":[{"type":"additional_tools","tools":[{"type":"function","name":"extra","parameters":{"type":"object"}}]},{"type":"message","role":"user","content":"x"}]}`, rctx())
	if len(r.Tools) != 1 || r.Tools[0].Name != "extra" {
		t.Fatalf("%+v", r.Tools)
	}
}

func TestResponsesWriterShape(t *testing.T) {
	long := strings.Repeat("a", 100)
	r := &translate.Request{Model: "m", Stream: true, Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart("rules")},
		{Role: translate.RoleUser, Content: []translate.Part{{Type: translate.PartText, Text: "hi"}, {Type: translate.PartImage, URL: "https://x.example/i.png", Detail: "low"}}},
		{Role: translate.RoleAssistant, Reasoning: "why", ReasoningSignature: "ENC", ToolCalls: []translate.ToolCall{{ID: long, Name: "f", Arguments: `{"a":1}`}}},
		{Role: translate.RoleTool, ToolCallID: long, Content: translate.TextPart("done")},
	}, Thinking: translate.Thinking{Mode: "level", Level: "high"}, Params: translate.Params{MaxTokens: func() *int { n := 90; return &n }()}}
	out, err := ResponsesCodec{}.WriteRequest(r, rctx())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["store"] != false || m["instructions"] != "rules" || m["stream"] != true || m["max_output_tokens"].(float64) != 90 {
		t.Fatalf("%s", out)
	}
	if m["reasoning"].(map[string]any)["effort"] != "high" {
		t.Errorf("effort: %v", m["reasoning"])
	}
	items := m["input"].([]any)
	var types []string
	for _, it := range items {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	if strings.Join(types, ",") != "message,reasoning,function_call,function_call_output" {
		t.Fatalf("items %v", types)
	}
	call := items[2].(map[string]any)
	if len(call["call_id"].(string)) != 64 || items[3].(map[string]any)["call_id"] != call["call_id"] {
		t.Errorf("a long call id must be clamped to 64 characters on both sides: %v / %v", call["call_id"], items[3])
	}
	if items[1].(map[string]any)["encrypted_content"] != "ENC" {
		t.Errorf("reasoning continuity lost: %v", items[1])
	}
}

func TestResponseRoundTripAndStatus(t *testing.T) {
	in := `{"id":"resp_1","object":"response","created_at":1700000000,"model":"gpt-5","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"think"}],"encrypted_content":"E"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"partial"}]}],"usage":{"input_tokens":20,"output_tokens":8,"input_tokens_details":{"cached_tokens":5},"output_tokens_details":{"reasoning_tokens":3}}}`
	c := ResponsesCodec{}
	resp, err := c.ReadResponse([]byte(in), rctx())
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != translate.FinishLength || resp.Message.Text() != "partial" || resp.Message.Reasoning != "think" || resp.Message.ReasoningSignature != "E" {
		t.Fatalf("%+v", resp)
	}
	if resp.Usage.Prompt != 20 || resp.Usage.Cached != 5 || resp.Usage.Reasoning != 3 {
		t.Errorf("%+v", resp.Usage)
	}
	out, _ := c.WriteResponse(resp, rctx())
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["status"] != "incomplete" || m["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" || m["object"] != "response" {
		t.Errorf("%s", out)
	}
}

func TestAFunctionCallAnswerFinishesWithToolCalls(t *testing.T) {
	in := `{"id":"r","model":"m","status":"completed","output":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"}]}`
	resp, _ := ResponsesCodec{}.ReadResponse([]byte(in), rctx())
	if resp.FinishReason != translate.FinishToolCalls {
		t.Fatalf("%q", resp.FinishReason)
	}
}

func TestOpenAIToResponsesToOpenAIKeepsTheConversation(t *testing.T) {
	in := `{"model":"gpt-5","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"res"}]}`
	c := translate.NewCtx(translate.FormatOpenAI, translate.FormatResponses)
	rs, _, err := translate.Convert(translate.FormatOpenAI, translate.FormatResponses, []byte(in), c)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := translate.Convert(translate.FormatResponses, translate.FormatOpenAI, rs, c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(back, &m)
	var roles []string
	for _, x := range m["messages"].([]any) {
		roles = append(roles, x.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool" {
		t.Fatalf("%v", roles)
	}
}
