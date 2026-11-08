package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func gctx() *Ctx { return NewCtx(FormatGemini, FormatGemini) }

type memSigs struct{ m map[string]string }

func (s *memSigs) Get(id string) (string, bool) { v, ok := s.m[id]; return v, ok }
func (s *memSigs) Put(id, sig string)           { s.m[id] = sig }

func geminiOut(t *testing.T, r *Request, c *Ctx) map[string]any {
	t.Helper()
	out, err := GeminiCodec{}.WriteRequest(r, c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return m
}

func TestAGeminiHistoryEndingOnAFunctionCallIsAnsweredWithAFunctionResponse(t *testing.T) {
	// the OpenAI side: an assistant call then its result
	r := &Request{Messages: []Message{user("go"), asst(ToolCall{ID: "c1", Name: "f", Arguments: `{"a":1}`}), result("c1", "ok")}}
	m := geminiOut(t, r, gctx())
	contents := m["contents"].([]any)
	last := contents[len(contents)-1].(map[string]any)
	if last["role"] != "user" {
		t.Fatalf("the contents end on %v", last["role"])
	}
	fr := last["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "f" || fr["id"] != "c1" {
		t.Fatalf("%v", fr)
	}
}

func TestContentsAlternateStartAndEndOnAUserTurn(t *testing.T) {
	r := &Request{Messages: []Message{
		{Role: RoleAssistant, Content: TextPart("I start")}, user("a"), user("b"), {Role: RoleAssistant, Content: TextPart("end")},
	}}
	contents := geminiOut(t, r, gctx())["contents"].([]any)
	if contents[0].(map[string]any)["role"] != "user" || contents[len(contents)-1].(map[string]any)["role"] != "user" {
		t.Fatalf("%v", contents)
	}
	for i := 1; i < len(contents); i++ {
		if contents[i].(map[string]any)["role"] == contents[i-1].(map[string]any)["role"] {
			t.Errorf("turns %d and %d share a role", i-1, i)
		}
	}
}

func TestASystemOnlyGeminiRequestBecomesAUserTurn(t *testing.T) {
	req, err := GeminiCodec{}.ReadRequest([]byte(`{"systemInstruction":{"parts":[{"text":"only this"}]}}`), gctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != RoleUser || req.Messages[0].Text() != "only this" {
		t.Fatalf("%+v", req.Messages)
	}
	// and an empty request still yields contents
	m := geminiOut(t, &Request{}, gctx())
	if len(m["contents"].([]any)) != 1 {
		t.Fatalf("%v", m["contents"])
	}
}

func TestEveryCallIDIsUniqueOverTheWholeHistoryAndResultsFollow(t *testing.T) {
	r := &Request{Messages: []Message{
		user("a"),
		asst(ToolCall{ID: "same", Name: "f", Arguments: "{}"}), result("same", "first"),
		asst(ToolCall{ID: "same", Name: "f", Arguments: "{}"}), result("same", "second"),
	}}
	contents := geminiOut(t, r, gctx())["contents"].([]any)
	var callIDs, respIDs []string
	var respText []string
	for _, c := range contents {
		for _, p := range c.(map[string]any)["parts"].([]any) {
			pm := p.(map[string]any)
			if fc, ok := pm["functionCall"].(map[string]any); ok {
				callIDs = append(callIDs, fc["id"].(string))
			}
			if fr, ok := pm["functionResponse"].(map[string]any); ok {
				respIDs = append(respIDs, fr["id"].(string))
				respText = append(respText, fr["response"].(map[string]any)["result"].(string))
			}
		}
	}
	if len(callIDs) != 2 || callIDs[0] == callIDs[1] {
		t.Fatalf("call ids %v", callIDs)
	}
	if fmt.Sprint(respIDs) != fmt.Sprint(callIDs) || fmt.Sprint(respText) != "[first second]" {
		t.Fatalf("responses %v %v do not follow calls %v", respIDs, respText, callIDs)
	}
}

func TestAResultWithoutAnIDAnswersTheOldestUnansweredCallOfItsName(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"go"}]},{"role":"model","parts":[{"functionCall":{"name":"f","args":{}}},{"functionCall":{"name":"g","args":{}}},{"functionCall":{"name":"f","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{"result":"one"}}},{"functionResponse":{"name":"f","response":{"result":"two"}}},{"functionResponse":{"name":"g","response":{"result":"g!"}}}]}]}`
	req, err := GeminiCodec{}.ReadRequest([]byte(body), gctx())
	if err != nil {
		t.Fatal(err)
	}
	calls := req.Messages[1].ToolCalls
	if len(calls) != 3 || calls[0].ID == "" || calls[0].ID == calls[2].ID {
		t.Fatalf("a call without an id must get a distinct one: %+v", calls)
	}
	var byID = map[string]string{}
	for _, m := range req.Messages {
		if m.Role == RoleTool {
			byID[m.ToolCallID] = m.Text()
		}
	}
	if byID[calls[0].ID] != "one" || byID[calls[2].ID] != "two" || byID[calls[1].ID] != "g!" {
		t.Fatalf("results %v for calls %+v", byID, calls)
	}
}

func TestThoughtPartsBecomeReasoning(t *testing.T) {
	req, _ := GeminiCodec{}.ReadRequest([]byte(`{"contents":[{"role":"model","parts":[{"text":"hmm","thought":true},{"text":"answer"}]}]}`), gctx())
	m := req.Messages[0]
	if m.Reasoning != "hmm" || m.Text() != "answer" {
		t.Fatalf("%+v", m)
	}
}

func TestSignaturesRideOnTheCallAndAreRestoredWhenAClientDropsThem(t *testing.T) {
	// the answer carries a signature; the store remembers it
	store := &memSigs{m: map[string]string{}}
	c := gctx()
	c.Calls = store
	resp, err := GeminiCodec{}.ReadResponse([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"c9","name":"f","args":{}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}]}`), c)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.ToolCalls[0].Signature != "SIG" || store.m["c9"] != "SIG" {
		t.Fatalf("%+v %v", resp.Message.ToolCalls, store.m)
	}
	// the client sends the call back without the signature: it is restored
	r := &Request{Messages: []Message{user("x"), asst(ToolCall{ID: "c9", Name: "f", Arguments: "{}"}), result("c9", "r")}}
	out, _ := GeminiCodec{}.WriteRequest(r, c)
	if !strings.Contains(string(out), `"thoughtSignature":"SIG"`) {
		t.Errorf("not restored: %s", out)
	}
	// a signature the client kept wins over the store
	r.Messages[1].ToolCalls[0].Signature = "CLIENT"
	out, _ = GeminiCodec{}.WriteRequest(r, c)
	if !strings.Contains(string(out), `"thoughtSignature":"CLIENT"`) || strings.Contains(string(out), `"thoughtSignature":"SIG"`) {
		t.Errorf("the client's signature lost: %s", out)
	}
	// no store, no signature, none invented
	out, _ = GeminiCodec{}.WriteRequest(&Request{Messages: []Message{user("x"), asst(ToolCall{ID: "z", Name: "f", Arguments: "{}"}), result("z", "r")}}, gctx())
	if strings.Contains(string(out), "thoughtSignature") {
		t.Errorf("a signature appeared from nowhere: %s", out)
	}
}

func TestSchemaCleaning(t *testing.T) {
	in := json.RawMessage(`{"$schema":"x","type":"object","title":"T","additionalProperties":false,"properties":{
	  "a":{"type":["string","null"],"default":"x"},
	  "b":{"const":5},
	  "c":{"type":"integer","enum":[1,2],"exclusiveMinimum":0},
	  "d":{"anyOf":[{"type":"null"},{"type":"number","minimum":1}]},
	  "e":{"allOf":[{"properties":{"x":{"type":"string"}},"required":["x"]},{"properties":{"y":{"type":"string"}}}]},
	  "f":{"type":"array","items":{"type":"object","properties":{"g":{"type":"string"}},"required":["g","ghost"]}}},
	 "required":["a","missing"]}`)
	var got map[string]any
	if err := json.Unmarshal(cleanGeminiSchema(in), &got); err != nil {
		t.Fatal(err)
	}
	flat, _ := json.Marshal(got)
	for _, banned := range []string{"$schema", "title", "additionalProperties", "default", "exclusiveMinimum", "allOf", "anyOf", "const", `"null"`} {
		if strings.Contains(string(flat), banned) {
			t.Errorf("%q survived: %s", banned, flat)
		}
	}
	props := got["properties"].(map[string]any)
	if props["a"].(map[string]any)["type"] != "string" {
		t.Errorf("a: %v", props["a"])
	}
	if b := props["b"].(map[string]any); b["type"] != "string" || b["enum"].([]any)[0] != "5" {
		t.Errorf("const: %v", b)
	}
	if c := props["c"].(map[string]any); c["enum"].([]any)[0] != "1" || c["type"] != "string" {
		t.Errorf("enum must be strings with an explicit string type: %v", c)
	}
	if d := props["d"].(map[string]any); d["type"] != "number" {
		t.Errorf("anyOf: %v", d)
	}
	if e := props["e"].(map[string]any); e["properties"].(map[string]any)["x"] == nil || e["properties"].(map[string]any)["y"] == nil {
		t.Errorf("allOf: %v", e)
	}
	if req := got["required"].([]any); len(req) != 1 || req[0] != "a" {
		t.Errorf("a required name that is not a property: %v", req)
	}
	items := props["f"].(map[string]any)["items"].(map[string]any)
	if req := items["required"].([]any); len(req) != 1 || req[0] != "g" {
		t.Errorf("nested required: %v", req)
	}
	// a bad schema falls back to an empty object schema
	for _, bad := range []string{``, `nope`, `[]`, `"x"`} {
		if got := string(cleanGeminiSchema(json.RawMessage(bad))); got != `{"type":"object","properties":{}}` {
			t.Errorf("%q: %s", bad, got)
		}
	}
	// deterministic: map order must not reach the bytes
	first := string(cleanGeminiSchema(in))
	for range 50 {
		if again := string(cleanGeminiSchema(in)); again != first {
			t.Fatalf("two cleanings differ:\n%s\n%s", first, again)
		}
	}
}

func TestFunctionNamesAreSanitized(t *testing.T) {
	for in, want := range map[string]string{"ok_name": "ok_name", "": "_unknown", "9lives": "_9lives", "has space": "has_space", "a/b": "a_b", "é": "_", strings.Repeat("x", 80): strings.Repeat("x", 64), "dot.colon:dash-": "dot.colon:dash-"} {
		if got := sanitizeGeminiName(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestGemini4DropsTheSamplingFields(t *testing.T) {
	temp := 0.7
	r := &Request{Model: "gemini-4-argon", Messages: []Message{user("x")}, Params: Params{Temperature: &temp}}
	if strings.Contains(string(mustMarshalReq(t, r)), "temperature") {
		t.Error("gemini 4 was sent a sampling parameter")
	}
	r.Model = "gemini-2.5-pro"
	if !strings.Contains(string(mustMarshalReq(t, r)), "temperature") {
		t.Error("an older model lost its temperature")
	}
}

func mustMarshalReq(t *testing.T, r *Request) []byte {
	t.Helper()
	out, err := GeminiCodec{}.WriteRequest(r, gctx())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGeminiToolChoiceAndThinking(t *testing.T) {
	r := &Request{Model: "m", Messages: []Message{user("x")}, ToolChoice: &ToolChoice{Mode: "tool", Name: "my tool"},
		Thinking: Thinking{Mode: "budget", Budget: 2048}}
	m := geminiOut(t, r, gctx())
	cfg := m["toolConfig"].(map[string]any)["functionCallingConfig"].(map[string]any)
	if cfg["mode"] != "ANY" || cfg["allowedFunctionNames"].([]any)[0] != "my_tool" {
		t.Errorf("%v", cfg)
	}
	th := m["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if th["thinkingBudget"].(float64) != 2048 || th["includeThoughts"] != true {
		t.Errorf("%v", th)
	}
	r.Thinking = Thinking{Mode: "none"}
	th = geminiOut(t, r, gctx())["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if th["thinkingBudget"].(float64) != 0 {
		t.Errorf("%v", th)
	}
	c := gctx()
	c.Caps.ThinkingFormat = "gemini-level"
	r.Thinking = Thinking{Mode: "level", Level: "high"}
	th = geminiOut(t, r, c)["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if th["thinkingLevel"] != "HIGH" {
		t.Errorf("%v", th)
	}
}

func TestGeminiUsageCountsThoughtsInTheCompletion(t *testing.T) {
	resp, _ := GeminiCodec{}.ReadResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":6,"cachedContentTokenCount":3}}`), gctx())
	if resp.Usage.Prompt != 10 || resp.Usage.Completion != 10 || resp.Usage.Reasoning != 6 || resp.Usage.Cached != 3 {
		t.Fatalf("%+v", resp.Usage)
	}
	if resp.FinishReason != FinishLength {
		t.Errorf("%q", resp.FinishReason)
	}
	// candidates derived from the total when missing
	resp, _ = GeminiCodec{}.ReadResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":"a"}]}}],"usageMetadata":{"promptTokenCount":10,"totalTokenCount":25,"thoughtsTokenCount":5}}`), gctx())
	if resp.Usage.Completion != 15 {
		t.Errorf("derived completion %d, want 15", resp.Usage.Completion)
	}
	out, _ := GeminiCodec{}.WriteResponse(resp, gctx())
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	u := m["usageMetadata"].(map[string]any)
	if u["thoughtsTokenCount"].(float64) != 5 || u["candidatesTokenCount"].(float64) != 10 {
		t.Errorf("%v", u)
	}
}

func TestSafetyFinishReasonsAreAContentFilter(t *testing.T) {
	for _, reason := range []string{"SAFETY", "RECITATION", "PROHIBITED_CONTENT"} {
		resp, _ := GeminiCodec{}.ReadResponse([]byte(`{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"`+reason+`"}]}`), gctx())
		if resp.FinishReason != FinishFilter {
			t.Errorf("%s: %q", reason, resp.FinishReason)
		}
	}
}

func TestOpenAIToGeminiToOpenAIKeepsTheConversation(t *testing.T) {
	in := `{"model":"gemini-2.5-pro","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"res"},{"role":"user","content":"thanks"}],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"number"}}}}}]}`
	c := NewCtx(FormatOpenAI, FormatGemini)
	gm, _, err := Convert(FormatOpenAI, FormatGemini, []byte(in), c)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := Convert(FormatGemini, FormatOpenAI, gm, c)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(back, &m)
	var roles []string
	for _, x := range m["messages"].([]any) {
		roles = append(roles, x.(map[string]any)["role"].(string))
	}
	if strings.Join(roles, ",") != "system,user,assistant,tool,user" {
		t.Fatalf("roles %v", roles)
	}
}

func TestGeminiBadBodiesAreRefused(t *testing.T) {
	for _, in := range []string{``, `nope`, `[]`, `{"contents":"x"}`, `{"contents":[1]}`} {
		if _, err := (GeminiCodec{}).ReadRequest([]byte(in), gctx()); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestAnEnumIsAlwaysAStringWhateverOtherTypeWasDeclared(t *testing.T) {
	// The type must not depend on the order a map happens to be walked in, so
	// the check is repeated: with the bug it flips between runs.
	for range 200 {
		var got map[string]any
		_ = json.Unmarshal(cleanGeminiSchema(json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","enum":[1,2,3]},"b":{"type":"boolean","enum":[true,false]}}}`)), &got)
		props := got["properties"].(map[string]any)
		for name, p := range props {
			if p.(map[string]any)["type"] != "string" {
				t.Fatalf("%s: type %v, want string", name, p.(map[string]any)["type"])
			}
		}
		if props["n"].(map[string]any)["enum"].([]any)[0] != "1" || props["b"].(map[string]any)["enum"].([]any)[0] != "true" {
			t.Fatalf("enum values must be strings: %v", props)
		}
	}
}
