package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"rllmx/app/llm/translate"
	"strings"
	"testing"

	"rllmx/app/llm/upstream"
)

func cctx() *translate.Ctx {
	c := translate.NewCtx(translate.FormatClaude, translate.FormatClaude)
	c.Caps = upstream.Capability{Tools: true, Vision: true, MaxOutput: 64000, ThinkingCanOff: true}
	c.BudgetOf = func(l string) int { return map[string]int{"low": 2048, "high": 24576}[l] }
	return c
}

func claudeOut(t *testing.T, body string) map[string]any {
	t.Helper()
	c := ClaudeCodec{}
	req, err := c.ReadRequest([]byte(body), cctx())
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.WriteRequest(req, cctx())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return m
}

// stripMarkers renders a decoded value without any cache_control field.
func stripMarkers(v any) string {
	var strip func(any) any
	strip = func(x any) any {
		switch t := x.(type) {
		case map[string]any:
			out := map[string]any{}
			for k, c := range t {
				if k != "cache_control" {
					out[k] = strip(c)
				}
			}
			return out
		case []any:
			out := make([]any, len(t))
			for i, c := range t {
				out[i] = strip(c)
			}
			return out
		}
		return x
	}
	b, _ := json.Marshal(strip(v))
	return string(b)
}

func countMarkers(v any) int {
	n := 0
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["cache_control"]; ok {
			n++
		}
		for _, c := range x {
			n += countMarkers(c)
		}
	case []any:
		for _, c := range x {
			n += countMarkers(c)
		}
	}
	return n
}

const agentLoop = `{"model":"claude-opus-5","max_tokens":1000,
"system":[{"type":"text","text":"be careful","cache_control":{"type":"ephemeral"}}],
"tools":[{"name":"read","description":"r","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}},{"name":"write","description":"w","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}],
"messages":[
 {"role":"user","content":[{"type":"text","text":"fix it","cache_control":{"type":"ephemeral"}}]},
 {"role":"assistant","content":[{"type":"text","text":"reading"},{"type":"tool_use","id":"t1","name":"read","input":{"p":"a"},"cache_control":{"type":"ephemeral"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"file","cache_control":{"type":"ephemeral"}}]},
 {"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"write","input":{"p":"b"},"cache_control":{"type":"ephemeral"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"done","cache_control":{"type":"ephemeral"}}]}
]}`

func TestAtMostFourCacheMarkersAreEverSent(t *testing.T) {
	out := claudeOut(t, agentLoop)
	if n := countMarkers(out); n > 4 || n == 0 {
		t.Fatalf("%d markers", n)
	}
}

func TestClientCacheMarkersAreNeverKept(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"b","cache_control":{"type":"ephemeral"}}]}]}`
	out := claudeOut(t, body)
	// the client sent two; the only marker left is the one we place on the last block
	if n := countMarkers(out); n != 1 {
		t.Fatalf("%d markers: %v", n, out)
	}
	blocks := out["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if _, ok := blocks[0].(map[string]any)["cache_control"]; ok {
		t.Error("a client marker survived on the first block")
	}
}

func TestSystemAndToolGetTheLongTTLAndAssistantGetsTheShortOne(t *testing.T) {
	out := claudeOut(t, agentLoop)
	sys := out["system"].([]any)
	if cc := sys[len(sys)-1].(map[string]any)["cache_control"].(map[string]any); cc["ttl"] != "1h" {
		t.Errorf("system: %v", cc)
	}
	tools := out["tools"].([]any)
	if cc := tools[len(tools)-1].(map[string]any)["cache_control"].(map[string]any); cc["ttl"] != "1h" {
		t.Errorf("last tool: %v", cc)
	}
	if _, ok := tools[0].(map[string]any)["cache_control"]; ok {
		t.Error("a tool before the last was marked")
	}
	// the last assistant block carries a five-minute marker
	found := false
	for _, m := range out["messages"].([]any) {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		for _, b := range mm["content"].([]any) {
			if cc, ok := b.(map[string]any)["cache_control"].(map[string]any); ok {
				found = true
				if _, hasTTL := cc["ttl"]; hasTTL {
					t.Errorf("the assistant marker has a ttl: %v", cc)
				}
			}
		}
	}
	if !found {
		t.Error("no assistant marker")
	}
}

func TestAConversationWithNoAssistantTurnMarksItsLastMessage(t *testing.T) {
	out := claudeOut(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"hello"}]}`)
	blocks := out["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if _, ok := blocks[len(blocks)-1].(map[string]any)["cache_control"]; !ok {
		t.Fatalf("%v", out)
	}
}

func TestAToolWithDeferredLoadingIsNeverMarked(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"tools":[{"name":"a","description":"x","input_schema":{"type":"object"}},{"type":"custom_server_tool","name":"b","defer_loading":true}],"messages":[{"role":"user","content":"x"}]}`
	out := claudeOut(t, body)
	tools := out["tools"].([]any)
	if _, ok := tools[1].(map[string]any)["cache_control"]; ok {
		t.Error("a defer_loading tool was marked")
	}
	if _, ok := tools[0].(map[string]any)["cache_control"]; !ok {
		t.Error("the last markable tool was not marked")
	}
}

// A tool-call conversation grows by appending. Everything before the new turn
// must encode to the same bytes, or the upstream cannot read its cache.
func TestThePrefixOfAGrowingConversationIsByteStable(t *testing.T) {
	c := ClaudeCodec{}
	base := func(extra string) []byte {
		body := `{"model":"claude-opus-5","max_tokens":100,"system":"sys","tools":[{"name":"f","description":"d","input_schema":{"type":"object"}}],"messages":[
		{"role":"user","content":"start"},
		{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"f","input":{"x":1}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"r"}]}` + extra + `]}`
		req, err := c.ReadRequest([]byte(body), cctx())
		if err != nil {
			t.Fatal(err)
		}
		out, _ := c.WriteRequest(req, cctx())
		return out
	}
	short := base("")
	long := base(`,{"role":"assistant","content":[{"type":"tool_use","id":"b","name":"f","input":{"x":2}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"r2"}]}`)
	var a, b struct {
		System json.RawMessage   `json:"system"`
		Tools  json.RawMessage   `json:"tools"`
		Msgs   []json.RawMessage `json:"messages"`
	}
	_ = json.Unmarshal(short, &a)
	_ = json.Unmarshal(long, &b)
	if !bytes.Equal(a.System, b.System) || !bytes.Equal(a.Tools, b.Tools) {
		t.Fatalf("system or tools changed between turns:\n%s\n%s", a.System, b.System)
	}
	// A marker moves to the newest assistant turn, so the marked message differs
	// by that one field. Everything else about the shared prefix is identical.
	strip := func(raw json.RawMessage) string {
		var v any
		_ = json.Unmarshal(raw, &v)
		return stripMarkers(v)
	}
	for i := 0; i < 3; i++ {
		if strip(a.Msgs[i]) != strip(b.Msgs[i]) {
			t.Errorf("message %d changed apart from its marker:\n%s\n%s", i, a.Msgs[i], b.Msgs[i])
		}
	}
	if !bytes.Equal(base(""), short) {
		t.Fatal("the same request encoded twice differently")
	}
}

func TestMaxTokensDefaultsWhenTheClientSentNone(t *testing.T) {
	out := claudeOut(t, `{"model":"m","messages":[{"role":"user","content":"x"}]}`)
	if out["max_tokens"] == nil || out["max_tokens"].(float64) <= 0 {
		t.Fatalf("%v", out["max_tokens"])
	}
}

func TestHistoryRepair(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[
	 {"role":"assistant","content":"I speak first"},
	 {"role":"user","content":"a"},{"role":"user","content":"b"},
	 {"role":"assistant","content":[{"type":"text","text":"x"},{"type":"tool_use","id":"t","name":"f","input":{}},{"type":"text","text":"after the call"}]},
	 {"role":"user","content":[{"type":"text","text":"note"},{"type":"tool_result","tool_use_id":"t","content":"r"}]},
	 {"role":"assistant","content":[{"type":"thinking","thinking":"unsigned"},{"type":"text","text":"ok"}]}
	]}`
	out := claudeOut(t, body)
	msgs := out["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "user" {
		t.Errorf("the conversation opens on %v", msgs[0].(map[string]any)["role"])
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].(map[string]any)["role"] == msgs[i-1].(map[string]any)["role"] {
			t.Errorf("messages %d and %d share a role", i-1, i)
		}
	}
	raw, _ := json.Marshal(out)
	// The hub keeps text and calls apart, so the writer emits the text first and
	// nothing ever follows a tool_use inside one assistant turn.
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "assistant" {
			continue
		}
		seenCall := false
		for _, b := range mm["content"].([]any) {
			typ := b.(map[string]any)["type"]
			if seenCall && typ == "text" {
				t.Errorf("text follows a tool_use: %v", mm["content"])
			}
			if typ == "tool_use" {
				seenCall = true
			}
		}
	}
	if strings.Contains(string(raw), "unsigned") {
		t.Error("an unsigned thinking block survived")
	}
	// the merged user turn puts the tool result first
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] != "user" {
			continue
		}
		blocks := mm["content"].([]any)
		for i, b := range blocks {
			if b.(map[string]any)["type"] == "tool_result" && i != 0 {
				t.Errorf("a tool_result is not first: %v", blocks)
			}
		}
	}
}

func TestASignedThinkingBlockIsKept(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"sig123"},{"type":"text","text":"a"}]},{"role":"user","content":"more"}]}`
	raw, _ := json.Marshal(claudeOut(t, body))
	if !strings.Contains(string(raw), "sig123") {
		t.Fatalf("%s", raw)
	}
}

func TestToolResultImagesGoToTheUserTurnOnTheWayIn(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"shot"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]}]}]}`
	req, err := claudeCodec(t).ReadRequest([]byte(body), cctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 2 || req.Messages[0].Role != translate.RoleTool || req.Messages[0].Text() != "shot" {
		t.Fatalf("%+v", req.Messages)
	}
	user := req.Messages[1]
	if user.Role != translate.RoleUser || !strings.Contains(user.Text(), "[Image from tool result t]") {
		t.Fatalf("%+v", user)
	}
	hasImage := false
	for _, p := range user.Content {
		if p.Type == translate.PartImage && p.Data == "AAA" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("the image vanished: %+v", user.Content)
	}
}

func TestBillingHeaderLineIsRemovedFromSystem(t *testing.T) {
	body := `{"model":"m","max_tokens":1,"system":[{"type":"text","text":"x-anthropic-billing-header: cch=abc\nreal instructions"},{"type":"text","text":"more"}],"messages":[{"role":"user","content":"x"}]}`
	req, _ := claudeCodec(t).ReadRequest([]byte(body), cctx())
	if got := req.Messages[0].Text(); got != "real instructions\nmore" {
		t.Fatalf("%q", got)
	}
}

func TestAMidConversationSystemTurnBecomesAUserInstruction(t *testing.T) {
	body := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"a"},{"role":"system","content":"be terse"},{"role":"user","content":"b"}]}`
	req, _ := claudeCodec(t).ReadRequest([]byte(body), cctx())
	var found bool
	for _, m := range req.Messages {
		if m.Role == translate.RoleUser && strings.Contains(m.Text(), "<instructions>") && strings.Contains(m.Text(), "be terse") {
			found = true
		}
		if m.Role == translate.RoleSystem {
			t.Error("a system turn survived mid-conversation")
		}
	}
	if !found {
		t.Fatalf("%+v", req.Messages)
	}
}

func TestThinkingFieldsRoundTrip(t *testing.T) {
	for body, want := range map[string]translate.Thinking{
		`{"model":"m","max_tokens":9,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[]}`: {Mode: "budget", Budget: 4096},
		`{"model":"m","max_tokens":9,"thinking":{"type":"disabled"},"messages":[]}`:                     {Mode: "none"},
		`{"model":"m","max_tokens":9,"thinking":{"type":"adaptive"},"messages":[]}`:                     {Mode: "auto"},
	} {
		req, _ := claudeCodec(t).ReadRequest([]byte(body), cctx())
		if req.Thinking != want {
			t.Errorf("%s: %+v, want %+v", body, req.Thinking, want)
		}
	}
	c := cctx()
	c.Caps.ThinkingOffType = "disabled_by_model"
	req := &translate.Request{Model: "m", Messages: []translate.Message{translate.Message{Role: translate.RoleUser, Content: translate.TextPart("x")}}, Thinking: translate.Thinking{Mode: "none"}}
	out, _ := claudeCodec(t).WriteRequest(req, c)
	if !strings.Contains(string(out), "disabled_by_model") {
		t.Errorf("the model's own off type was not used: %s", out)
	}
}

func TestAdaptiveThinkingCarriesTheLevelInOutputConfig(t *testing.T) {
	c := cctx()
	c.Caps.ThinkingFormat = "claude-adaptive"
	out, _ := claudeCodec(t).WriteRequest(&translate.Request{Model: "m", Messages: []translate.Message{translate.Message{Role: translate.RoleUser, Content: translate.TextPart("x")}}, Thinking: translate.Thinking{Mode: "level", Level: "high"}}, c)
	if !strings.Contains(string(out), `"type":"adaptive"`) || !strings.Contains(string(out), `"effort":"high"`) {
		t.Fatalf("%s", out)
	}
	c.Caps.ThinkingFormat = "claude-budget"
	out, _ = claudeCodec(t).WriteRequest(&translate.Request{Model: "m", Messages: []translate.Message{translate.Message{Role: translate.RoleUser, Content: translate.TextPart("x")}}, Thinking: translate.Thinking{Mode: "level", Level: "high"}}, c)
	if !strings.Contains(string(out), `"budget_tokens":24576`) {
		t.Fatalf("%s", out)
	}
}

func TestForcedToolChoiceIsRelaxedWhileThinking(t *testing.T) {
	c := cctx()
	c.Caps.RelaxToolChoice = true
	r := &translate.Request{Model: "m", Messages: []translate.Message{translate.Message{Role: translate.RoleUser, Content: translate.TextPart("x")}}, ToolChoice: &translate.ToolChoice{Mode: "tool", Name: "f"}, Thinking: translate.Thinking{Mode: "budget", Budget: 2000}}
	out, _ := claudeCodec(t).WriteRequest(r, c)
	if !strings.Contains(string(out), `"tool_choice":{"type":"auto"}`) {
		t.Errorf("not relaxed: %s", out)
	}
	r.Thinking = translate.Thinking{}
	out, _ = claudeCodec(t).WriteRequest(r, c)
	if !strings.Contains(string(out), `"type":"tool"`) {
		t.Errorf("relaxed with no thinking: %s", out)
	}
}

func TestOutputConfigAndContextManagementFollowTheCapabilityRow(t *testing.T) {
	body := `{"model":"m","max_tokens":9,"messages":[{"role":"user","content":"x"}],"output_config":{"format":"x"},"context_management":{"edits":[]}}`
	c := cctx()
	c.Caps.DropOutputConfig = true
	req, _ := claudeCodec(t).ReadRequest([]byte(body), c)
	out, _ := claudeCodec(t).WriteRequest(req, c)
	if strings.Contains(string(out), "output_config") || strings.Contains(string(out), "context_management") {
		t.Errorf("both should go: %s", out)
	}
	c.Caps.DropOutputConfig = false
	c.Caps.KeepContextMgmt = true
	out, _ = claudeCodec(t).WriteRequest(req, c)
	if !strings.Contains(string(out), "output_config") || !strings.Contains(string(out), "context_management") {
		t.Errorf("both should stay: %s", out)
	}
}

func TestServerToolTypesAreFilteredOutsideAnthropic(t *testing.T) {
	body := `{"model":"m","max_tokens":9,"tools":[{"name":"f","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}],"messages":[{"role":"user","content":"x"}]}`
	c := cctx()
	req, _ := claudeCodec(t).ReadRequest([]byte(body), c)
	out, _ := claudeCodec(t).WriteRequest(req, c)
	if !strings.Contains(string(out), "web_search") {
		t.Fatalf("a server tool was dropped for Anthropic: %s", out)
	}
	c.Caps.ClaudeToolTypes = []string{"custom"}
	out, _ = claudeCodec(t).WriteRequest(req, c)
	if strings.Contains(string(out), "web_search") {
		t.Errorf("a server tool reached a provider that does not list it: %s", out)
	}
}

func TestResponseRoundTripAndUsage(t *testing.T) {
	in := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"thinking","thinking":"hm","signature":"s"},{"type":"text","text":"hi"},{"type":"tool_use","id":"tu","name":"f","input":{"a":1}}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`
	c := ClaudeCodec{}
	resp, err := c.ReadResponse([]byte(in), cctx())
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != translate.FinishToolCalls {
		t.Errorf("a stop after a tool call: %q", resp.FinishReason)
	}
	if resp.Usage.Prompt != 15 || resp.Usage.Cached != 3 || resp.Usage.CacheWrite != 2 || resp.Usage.Completion != 5 {
		t.Errorf("usage %+v: the prompt is the input plus the cache counts", resp.Usage)
	}
	out, _ := c.WriteResponse(resp, cctx())
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	u := m["usage"].(map[string]any)
	if u["input_tokens"].(float64) != 10 || u["cache_read_input_tokens"].(float64) != 3 {
		t.Errorf("usage out %v", u)
	}
	if m["stop_reason"] != "tool_use" || m["type"] != "message" {
		t.Errorf("%v", m)
	}
}

func TestOpenAIToClaudeToOpenAIKeepsTheConversation(t *testing.T) {
	in := `{"model":"claude-opus-5","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"res"},{"role":"user","content":"thanks"}],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}}],"max_tokens":100}`
	cl, req, err := translate.Convert(translate.FormatOpenAI, translate.FormatClaude, []byte(in), cctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 5 {
		t.Fatalf("hub: %d messages", len(req.Messages))
	}
	back, _, err := translate.Convert(translate.FormatClaude, translate.FormatOpenAI, cl, cctx())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(back, &m)
	msgs := m["messages"].([]any)
	roles := []string{}
	for _, x := range msgs {
		roles = append(roles, x.(map[string]any)["role"].(string))
	}
	want := []string{"system", "user", "assistant", "tool", "user"}
	if strings.Join(roles, ",") != strings.Join(want, ",") {
		t.Fatalf("roles %v, want %v", roles, want)
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "c1" || call["function"].(map[string]any)["arguments"] != `{"a":1}` {
		t.Errorf("tool call %v", call)
	}
}

func TestClaudeBadBodiesAreRefused(t *testing.T) {
	for _, in := range []string{``, `nope`, `[]`, `{"messages":"x"}`, `{"messages":[3]}`} {
		if _, err := (ClaudeCodec{}).ReadRequest([]byte(in), cctx()); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

// parallelResults is an agent loop whose last turn answers many calls at once:
// the case where the budget of four markers is actually reached.
func parallelResults(n int) string {
	var results []string
	for i := 0; i < n; i++ {
		results = append(results, fmt.Sprintf(`{"type":"tool_result","tool_use_id":"t%d","content":"r%d","cache_control":{"type":"ephemeral"}}`, i, i))
	}
	var calls []string
	for i := 0; i < n; i++ {
		calls = append(calls, fmt.Sprintf(`{"type":"tool_use","id":"t%d","name":"f","input":{}}`, i))
	}
	return `{"model":"claude-opus-5","max_tokens":100,"system":"sys","tools":[{"name":"f","description":"d","input_schema":{"type":"object"}}],"messages":[
	 {"role":"user","content":"go"},
	 {"role":"assistant","content":[` + strings.Join(calls, ",") + `]},
	 {"role":"user","content":[` + strings.Join(results, ",") + `]}]}`
}

func TestTheMarkerBudgetHoldsWhenManyToolResultsEndTheConversation(t *testing.T) {
	for _, n := range []int{1, 2, 3, 6, 20} {
		out := claudeOut(t, parallelResults(n))
		if got := countMarkers(out); got > maxCacheMarkers {
			t.Errorf("%d parallel results: %d markers, the limit is %d", n, got, maxCacheMarkers)
		}
	}
	// With room left, the final results are marked so the next turn can read them.
	out := claudeOut(t, parallelResults(6))
	last := out["messages"].([]any)
	results := last[len(last)-1].(map[string]any)["content"].([]any)
	marked := 0
	for _, r := range results {
		if _, ok := r.(map[string]any)["cache_control"]; ok {
			marked++
		}
	}
	if marked == 0 {
		t.Error("the final tool results carry no marker: the next turn cannot read them from the cache")
	}
	if got := countMarkers(out); got != maxCacheMarkers {
		t.Errorf("with room for four, %d were translate.Set", got)
	}
}

func TestOnlyTheFinalUserTurnsResultsAreMarkedNotEarlierOnes(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"messages":[
	 {"role":"user","content":"go"},
	 {"role":"assistant","content":[{"type":"tool_use","id":"a","name":"f","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"old"}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"b","name":"f","input":{}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"new"}]}]}`
	out := claudeOut(t, body)
	msgs := out["messages"].([]any)
	oldResult := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, ok := oldResult["cache_control"]; ok {
		t.Error("an earlier tool result carries a marker: it would move on the next turn and break the cache")
	}
}

func TestAClientMarkerOnAToolDefinitionNeverSurvivesWhereWeDidNotPutOne(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"tools":[{"type":"web_search_20250305","name":"web_search","cache_control":{"type":"ephemeral","ttl":"1h"}},{"name":"f","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"x"}]}`
	out := claudeOut(t, body)
	tools := out["tools"].([]any)
	if _, ok := tools[0].(map[string]any)["cache_control"]; ok {
		t.Error("a client marker on a raw tool survived")
	}
}

func TestAToolResultAlwaysCarriesIsError(t *testing.T) {
	in := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":null,"tool_calls":[` +
		`{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},` +
		`{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"boom","is_error":true},` +
		`{"role":"tool","tool_call_id":"c2","content":"ok"}]}`
	out, req, err := translate.Convert(translate.FormatOpenAI, translate.FormatClaude, []byte(in), cctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 4 || !req.Messages[2].Error || req.Messages[3].Error {
		t.Fatalf("hub: %+v", req.Messages)
	}
	body := string(out)
	if !strings.Contains(body, `"is_error":true`) || !strings.Contains(body, `"is_error":false`) {
		t.Fatalf("%s", body)
	}
	// A Claude client's own is_error survives the hub and is written again.
	claudeIn := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"f","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"boom","is_error":true}]}]}`
	_, hub, err := translate.Convert(translate.FormatClaude, translate.FormatClaude, []byte(claudeIn), cctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(hub.Messages) != 3 || !hub.Messages[2].Error {
		t.Fatalf("read: %+v", hub.Messages)
	}
}
