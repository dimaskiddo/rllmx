package translate

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
)

func asst(calls ...ToolCall) Message { return Message{Role: RoleAssistant, ToolCalls: calls} }
func result(id, text string) Message {
	return Message{Role: RoleTool, ToolCallID: id, Content: TextPart(text)}
}
func user(text string) Message { return Message{Role: RoleUser, Content: TextPart(text)} }

func TestToolIDs(t *testing.T) {
	r := &Request{Messages: []Message{
		user("go"),
		asst(ToolCall{ID: "ok_1-a", Name: "f"}, ToolCall{ID: "bad id!", Name: "g"}, ToolCall{ID: "", Name: "h-x"}, ToolCall{ID: "!!!", Name: ""}),
		result("ok_1-a", "1"), result("bad id!", "2"), result("", "3"), result("!!!", "4"),
	}}
	EnsureToolIDs(r)
	calls := r.Messages[1].ToolCalls
	want := []string{"ok_1-a", "badid", "call_msg1_tc2_h-x", "call_msg1_tc3"}
	for i, w := range want {
		if calls[i].ID != w {
			t.Errorf("call %d: %q, want %q", i, calls[i].ID, w)
		}
	}
	for i := 2; i < 6; i++ {
		if got := r.Messages[i].ToolCallID; got != want[i-2] {
			t.Errorf("result %d answers %q, want %q: a rewritten id left its result behind", i, got, want[i-2])
		}
	}
}

func TestDuplicateToolIDsBecomeUniqueAndStayPaired(t *testing.T) {
	r := &Request{Messages: []Message{
		user("go"),
		asst(ToolCall{ID: "x", Name: "f"}, ToolCall{ID: "x", Name: "f"}),
		result("x", "first"), result("x", "second"),
	}}
	EnsureToolIDs(r)
	a, b := r.Messages[1].ToolCalls[0].ID, r.Messages[1].ToolCalls[1].ID
	if a == b {
		t.Fatalf("two calls share %q", a)
	}
	if r.Messages[2].ToolCallID != a || r.Messages[3].ToolCallID != b {
		t.Fatalf("results %q %q, calls %q %q", r.Messages[2].ToolCallID, r.Messages[3].ToolCallID, a, b)
	}
}

func TestIDsAreUniqueAcrossTheWholeHistory(t *testing.T) {
	r := &Request{Messages: []Message{
		asst(ToolCall{ID: "same", Name: "f"}), result("same", "a"),
		asst(ToolCall{ID: "same", Name: "f"}), result("same", "b"),
	}}
	EnsureToolIDs(r)
	if r.Messages[0].ToolCalls[0].ID == r.Messages[2].ToolCalls[0].ID {
		t.Fatal("two turns share an id")
	}
	if r.Messages[1].ToolCallID != r.Messages[0].ToolCalls[0].ID || r.Messages[3].ToolCallID != r.Messages[2].ToolCalls[0].ID {
		t.Fatal("a result lost its call")
	}
}

func TestMissingToolResultsAreAdded(t *testing.T) {
	r := &Request{Messages: []Message{
		user("go"),
		asst(ToolCall{ID: "a"}, ToolCall{ID: "b"}, ToolCall{ID: "c"}),
		result("b", "only b"),
		user("then"),
	}}
	FixMissingToolResults(r)
	got := []string{}
	for _, m := range r.Messages {
		if m.Role == RoleTool {
			got = append(got, m.ToolCallID)
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("results for %v", got)
	}
	if r.Messages[len(r.Messages)-1].Role != RoleUser || r.Messages[len(r.Messages)-1].Text() != "then" {
		t.Fatal("the user turn moved")
	}
	for _, m := range r.Messages {
		if m.Role == RoleTool && m.ToolCallID != "b" && m.Text() != placeholderResult {
			t.Errorf("placeholder %q", m.Text())
		}
	}
	// idempotent
	n := len(r.Messages)
	FixMissingToolResults(r)
	if len(r.Messages) != n {
		t.Fatal("a second pass added more")
	}
}

func TestToolCallInvariantsHoldForRandomHistories(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	names := []string{"", "f", "g h", "x-y", "é"}
	for iter := 0; iter < 300; iter++ {
		r := &Request{}
		for i, n := 0, rng.Intn(8); i < n; i++ {
			switch rng.Intn(3) {
			case 0:
				r.Messages = append(r.Messages, user("u"))
			case 1:
				var calls []ToolCall
				for j, k := 0, rng.Intn(4); j < k; j++ {
					calls = append(calls, ToolCall{ID: fmt.Sprintf("id%d", rng.Intn(3)), Name: names[rng.Intn(len(names))]})
				}
				r.Messages = append(r.Messages, asst(calls...))
			default:
				r.Messages = append(r.Messages, result(fmt.Sprintf("id%d", rng.Intn(3)), "r"))
			}
		}
		EnsureToolIDs(r)
		FixMissingToolResults(r)
		seen := map[string]bool{}
		for i, m := range r.Messages {
			for _, tc := range m.ToolCalls {
				if !toolIDPattern.MatchString(tc.ID) {
					t.Fatalf("iter %d: call id %q is not valid", iter, tc.ID)
				}
				if seen[tc.ID] {
					t.Fatalf("iter %d: id %q repeated", iter, tc.ID)
				}
				seen[tc.ID] = true
				answered := false
				for _, n := range r.Messages[i+1:] {
					if n.Role == RoleTool && n.ToolCallID == tc.ID {
						answered = true
					}
					if n.Role != RoleTool {
						break
					}
				}
				if !answered {
					t.Fatalf("iter %d: call %q has no result right after it", iter, tc.ID)
				}
			}
		}
	}
}

func TestFinishReasons(t *testing.T) {
	for in, want := range map[string]string{"end_turn": "stop", "stop_sequence": "stop", "": "stop", "max_tokens": "length", "tool_use": "tool_calls", "refusal": "content_filter", "weird": "stop"} {
		if got := FinishFromClaude(in); got != want {
			t.Errorf("claude %q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"stop": "end_turn", "length": "max_tokens", "tool_calls": "tool_use", "content_filter": "refusal", "": "end_turn"} {
		if got := FinishToClaude(in); got != want {
			t.Errorf("to claude %q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"STOP": "stop", "MAX_TOKENS": "length", "SAFETY": "content_filter", "RECITATION": "content_filter", "PROHIBITED_CONTENT": "content_filter", "OTHER": "stop", "": "stop"} {
		if got := FinishFromGemini(in); got != want {
			t.Errorf("gemini %q: %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"stop": "STOP", "length": "MAX_TOKENS", "content_filter": "SAFETY", "tool_calls": "STOP"} {
		if got := FinishToGemini(in); got != want {
			t.Errorf("to gemini %q: %q, want %q", in, got, want)
		}
	}
}

func TestAFinalStopBecomesToolCallsOnlyWhenCallsWereEmitted(t *testing.T) {
	if got := FinishWithCalls("stop", 2); got != "tool_calls" {
		t.Errorf("%q", got)
	}
	if got := FinishWithCalls("", 1); got != "tool_calls" {
		t.Errorf("%q", got)
	}
	if got := FinishWithCalls("stop", 0); got != "stop" {
		t.Errorf("%q", got)
	}
	if got := FinishWithCalls("length", 3); got != "length" {
		t.Errorf("a length stop was rewritten: %q", got)
	}
	if got := FinishWithCalls("content_filter", 3); got != "content_filter" {
		t.Errorf("a filter stop was rewritten: %q", got)
	}
}

func reasoningCaps() upstream.Capability {
	return upstream.Capability{Reasoning: true, Tools: true, ThinkingCanOff: true, MaxOutput: 8000}
}

func TestThinkingPrecedence(t *testing.T) {
	c := reasoningCaps()
	suffix := routing.Thinking{Mode: "level", Level: "high"}
	client := Thinking{Mode: "level", Level: "low"}
	model := ModelThinking{Default: "medium", Fixed: ""}
	if got := ResolveThinking(suffix, client, model, c); got.Level != "high" {
		t.Errorf("suffix should beat the client: %+v", got)
	}
	if got := ResolveThinking(routing.Thinking{}, client, model, c); got.Level != "low" {
		t.Errorf("client should beat the default: %+v", got)
	}
	if got := ResolveThinking(routing.Thinking{}, Thinking{}, model, c); got.Level != "medium" {
		t.Errorf("default: %+v", got)
	}
	if got := ResolveThinking(suffix, client, ModelThinking{Fixed: "max", Default: "low"}, c); got.Level != "max" {
		t.Errorf("fixed should beat everything: %+v", got)
	}
	if got := ResolveThinking(routing.Thinking{}, Thinking{}, ModelThinking{}, c); got.Set() {
		t.Errorf("nothing asked: %+v", got)
	}
	if got := ResolveThinking(suffix, client, model, upstream.Capability{}); got.Set() {
		t.Errorf("a model that does not reason got thinking: %+v", got)
	}
	if got := ResolveThinking(routing.Thinking{}, Thinking{}, ModelThinking{Default: "8192"}, c); got.Mode != "budget" || got.Budget != 8192 {
		t.Errorf("a budget default: %+v", got)
	}
}

func TestThinkingIsDroppedWhenTheLastTurnIsNotTheUsers(t *testing.T) {
	r := &Request{Thinking: Thinking{Mode: "level", Level: "high"}, Messages: []Message{user("x"), {Role: RoleAssistant, Content: TextPart("prefill")}}}
	DropThinkingForPrefill(r)
	if r.Thinking.Set() {
		t.Error("thinking kept over an assistant prefill")
	}
	r = &Request{Thinking: Thinking{Mode: "level", Level: "high"}, Messages: []Message{user("x")}}
	DropThinkingForPrefill(r)
	if !r.Thinking.Set() {
		t.Error("thinking dropped for a user turn")
	}
}

func TestStripUnreadableKeepsTheTurn(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: RoleUser, Content: []Part{{Type: PartText, Text: "see"}, {Type: PartImage, URL: "u"}, {Type: PartAudio}, {Type: PartFile, MediaType: "application/pdf"}}},
		{Role: RoleUser, Content: []Part{{Type: PartImage, URL: "only"}}},
	}}
	StripUnreadable(r, upstream.Capability{})
	if len(r.Messages[0].Content) != 1 || r.Messages[0].Content[0].Text != "see" {
		t.Errorf("%+v", r.Messages[0].Content)
	}
	if len(r.Messages) != 2 || len(r.Messages[1].Content) != 1 || r.Messages[1].Content[0].Type != PartText {
		t.Errorf("an emptied message must stay as an empty text turn: %+v", r.Messages[1])
	}
	r2 := &Request{Messages: []Message{{Role: RoleUser, Content: []Part{{Type: PartImage, URL: "u"}, {Type: PartFile, MediaType: "video/mp4"}}}}}
	StripUnreadable(r2, upstream.Capability{Vision: true})
	if len(r2.Messages[0].Content) != 1 || r2.Messages[0].Content[0].Type != PartImage {
		t.Errorf("a readable image was removed or a video kept: %+v", r2.Messages[0].Content)
	}
}

func TestMaxTokensIsCutAndKeptAboveTheThinkingBudget(t *testing.T) {
	ptr := func(n int) *int { return &n }
	c := upstream.Capability{MaxOutput: 10000}
	r := &Request{Params: Params{MaxTokens: ptr(50000)}}
	AdjustMaxTokens(r, c)
	if *r.Params.MaxTokens != 10000 {
		t.Errorf("cut: %d", *r.Params.MaxTokens)
	}
	r = &Request{Params: Params{MaxTokens: ptr(2000)}, Thinking: Thinking{Mode: "budget", Budget: 4000}}
	AdjustMaxTokens(r, c)
	if *r.Params.MaxTokens <= 4000 {
		t.Errorf("not above the budget: %d", *r.Params.MaxTokens)
	}
	r = &Request{Params: Params{MaxTokens: ptr(2000)}, Thinking: Thinking{Mode: "budget", Budget: 9500}}
	AdjustMaxTokens(r, c)
	if *r.Params.MaxTokens > 10000 {
		t.Errorf("raised past the model's limit: %d", *r.Params.MaxTokens)
	}
	r = &Request{}
	AdjustMaxTokens(r, c)
	if r.Params.MaxTokens != nil {
		t.Error("a request with no limit was given one")
	}
	r = &Request{Params: Params{MaxTokens: ptr(500)}}
	AdjustMaxTokens(r, c)
	if *r.Params.MaxTokens != 500 {
		t.Errorf("a small limit changed: %d", *r.Params.MaxTokens)
	}
}

func TestApplyParams(t *testing.T) {
	body := []byte(`{"model":"m","temperature":0.5,"top_p":1,"max_tokens":9000,"messages":[{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"reasoning_content":"r"},{"role":"user","content":[{"type":"text","text":"x"},{"type":"image_url","image_url":{"url":"u"}}]}],"old":1}`)
	rules := []upstream.ParamRule{
		{Drop: []string{"temperature", "top_p"}},
		{Rename: map[string]string{"old": "new"}},
		{DropMessageFields: []string{"reasoning_content"}, FlattenContent: true},
		{CapOutput: 4096},
	}
	var got map[string]any
	if err := json.Unmarshal(ApplyParams(body, rules), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["temperature"]; ok {
		t.Error("temperature kept")
	}
	if got["new"] != float64(1) || got["old"] != nil {
		t.Errorf("rename: %v", got)
	}
	if got["max_tokens"] != float64(4096) {
		t.Errorf("cap: %v", got["max_tokens"])
	}
	msgs := got["messages"].([]any)
	a := msgs[0].(map[string]any)
	if a["content"] != "ab" || a["reasoning_content"] != nil {
		t.Errorf("assistant: %v", a)
	}
	if _, isString := msgs[1].(map[string]any)["content"].(string); isString {
		t.Error("a content array holding an image was flattened")
	}
}

func TestApplyParamsLeavesAnUnreadableBodyAndNoRulesAlone(t *testing.T) {
	if got := ApplyParams([]byte(`not json`), []upstream.ParamRule{{Drop: []string{"a"}}}); string(got) != "not json" {
		t.Errorf("%s", got)
	}
	in := []byte(`{"b":1,"a":2}`)
	if got := ApplyParams(in, nil); string(got) != string(in) {
		t.Errorf("no rules changed the body: %s", got)
	}
}

func TestParamsRunLastSoTheyDropWhatAnInjectionAdded(t *testing.T) {
	r := &Request{Model: "o3", Messages: []Message{user("x")}, Extra: map[string]json.RawMessage{"temperature": json.RawMessage("0.9")}}
	out, err := OpenAICodec{}.WriteRequest(r, ctx())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "temperature") {
		t.Fatal("the injection did not reach the body; the test would prove nothing")
	}
	final := Finish(out, []upstream.ParamRule{{Drop: []string{"temperature"}}})
	if strings.Contains(string(final), "temperature") {
		t.Fatalf("a rule applied before the injection: %s", final)
	}
}

func TestPrepareAppliesTheConcernsTogether(t *testing.T) {
	ptr := func(n int) *int { return &n }
	r := &Request{
		Params: Params{MaxTokens: ptr(90000)},
		Messages: []Message{
			{Role: RoleUser, Content: []Part{{Type: PartText, Text: "x"}, {Type: PartImage, URL: "u"}}},
			asst(ToolCall{ID: "bad id", Name: "f"}),
			user("next"),
		},
	}
	c := NewCtx(FormatOpenAI, FormatOpenAI)
	c.Caps = upstream.Capability{MaxOutput: 8000, Tools: true}
	Prepare(r, c)
	if *r.Params.MaxTokens != 8000 {
		t.Errorf("max tokens %d", *r.Params.MaxTokens)
	}
	if len(r.Messages[0].Content) != 1 {
		t.Errorf("the image stayed for a text-only model")
	}
	if r.Messages[1].ToolCalls[0].ID != "badid" || r.Messages[2].Role != RoleTool {
		t.Errorf("tool calls: %+v", r.Messages[1:])
	}
}
