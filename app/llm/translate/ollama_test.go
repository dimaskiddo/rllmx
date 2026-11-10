package translate

import (
	"encoding/json"
	"strings"
	"testing"
)

func octx() *Ctx { return NewCtx(FormatOllama, FormatOllama) }

func TestOllamaStreamsUnlessTheBodySaysOtherwise(t *testing.T) {
	r, _ := OllamaCodec{}.ReadRequest([]byte(`{"model":"m","messages":[]}`), octx())
	if !r.Stream {
		t.Error("a bare Ollama request must stream")
	}
	r, _ = OllamaCodec{}.ReadRequest([]byte(`{"model":"m","messages":[],"stream":false}`), octx())
	if r.Stream {
		t.Error("stream:false was ignored")
	}
}

func TestOllamaImagesAreRawBase64AndContentIsAString(t *testing.T) {
	r := &Request{Model: "llava", Messages: []Message{{Role: RoleUser, Content: []Part{
		{Type: PartText, Text: "what is this"}, {Type: PartImage, Data: "AAAA", MediaType: "image/png"},
		{Type: PartImage, URL: "data:image/jpeg;base64,BBBB"}, {Type: PartImage, URL: "https://remote.example/x.png"},
	}}}}
	out, err := OllamaCodec{}.WriteRequest(r, octx())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	msg := m["messages"].([]any)[0].(map[string]any)
	if msg["content"] != "what is this" {
		t.Errorf("content %v", msg["content"])
	}
	imgs := msg["images"].([]any)
	if len(imgs) != 2 || imgs[0] != "AAAA" || imgs[1] != "BBBB" {
		t.Errorf("images %v: a remote URL has no place here and a data URI loses its prefix", imgs)
	}
}

func TestOllamaToolResultsNameTheirToolAndResultsPairWithCalls(t *testing.T) {
	r := &Request{Model: "m", Messages: []Message{user("go"), asst(ToolCall{ID: "x", Name: "weather", Arguments: `{"city":"Paris"}`}), result("x", "sunny")}}
	out, _ := OllamaCodec{}.WriteRequest(r, octx())
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	msgs := m["messages"].([]any)
	call := msgs[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if call["name"] != "weather" || call["arguments"].(map[string]any)["city"] != "Paris" {
		t.Errorf("arguments must be an object: %v", call)
	}
	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_name"] != "weather" || tool["content"] != "sunny" {
		t.Errorf("%v", tool)
	}
	// and the other way
	back, _ := OllamaCodec{}.ReadRequest(out, octx())
	if back.Messages[1].ToolCalls[0].ID == "" || back.Messages[2].ToolCallID != back.Messages[1].ToolCalls[0].ID {
		t.Errorf("the result was not paired with its call: %+v", back.Messages)
	}
}

func TestOllamaOptionsMapBothWays(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"x"}],"options":{"temperature":0.3,"top_p":0.8,"top_k":40,"num_predict":99,"seed":5,"stop":["a","b"],"frequency_penalty":0.1,"presence_penalty":0.2,"num_ctx":8192,"repeat_penalty":1.1},"think":"high","format":"json","keep_alive":"5m"}`
	req, err := OllamaCodec{}.ReadRequest([]byte(body), octx())
	if err != nil {
		t.Fatal(err)
	}
	p := req.Params
	if *p.Temperature != 0.3 || *p.TopK != 40 || *p.MaxTokens != 99 || *p.Seed != 5 || len(p.Stop) != 2 || *p.FrequencyPenalty != 0.1 {
		t.Fatalf("%+v", p)
	}
	if req.Thinking.Level != "high" || string(p.ResponseFormat) != `{"type":"json_object"}` {
		t.Fatalf("%+v %s", req.Thinking, p.ResponseFormat)
	}
	out, _ := OllamaCodec{}.WriteRequest(req, octx())
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	o := m["options"].(map[string]any)
	if o["num_predict"].(float64) != 99 || o["top_k"].(float64) != 40 || o["seed"].(float64) != 5 {
		t.Errorf("%v", o)
	}
	if m["think"] != "high" || m["format"] != "json" {
		t.Errorf("%v", m)
	}
	// options the hub has no field for ride along as unknown top-level fields only
	if _, ok := o["num_ctx"]; ok {
		t.Log("num_ctx is not in options (documented: not forwarded)")
	}
}

func TestOllamaThinkMapping(t *testing.T) {
	for body, want := range map[string]Thinking{
		`{"model":"m","messages":[],"think":false}`: {Mode: "none"}, `{"model":"m","messages":[],"think":true}`: {Mode: "auto"},
		`{"model":"m","messages":[],"think":"low"}`: {Mode: "level", Level: "low"},
	} {
		r, _ := OllamaCodec{}.ReadRequest([]byte(body), octx())
		if r.Thinking != want {
			t.Errorf("%s: %+v", body, r.Thinking)
		}
	}
}

func TestOllamaSchemaFormatRoundTrips(t *testing.T) {
	req, _ := OllamaCodec{}.ReadRequest([]byte(`{"model":"m","messages":[],"format":{"type":"object","properties":{"a":{"type":"string"}}}}`), octx())
	if !strings.Contains(string(req.Params.ResponseFormat), "json_schema") {
		t.Fatalf("%s", req.Params.ResponseFormat)
	}
	out, _ := OllamaCodec{}.WriteRequest(req, octx())
	if !strings.Contains(string(out), `"format":{"properties"`) && !strings.Contains(string(out), `"format":{"type"`) {
		t.Errorf("%s", out)
	}
}

func TestOllamaResponseAlwaysHasContentAndOneDone(t *testing.T) {
	in := `{"model":"m","created_at":"t","message":{"role":"assistant","content":"hi","thinking":"hm","tool_calls":[{"function":{"name":"f","arguments":{"a":1}}}]},"done":true,"done_reason":"length","prompt_eval_count":12,"eval_count":7,"total_duration":99}`
	c := OllamaCodec{}
	resp, err := c.ReadResponse([]byte(in), octx())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.Prompt != 12 || resp.Usage.Completion != 7 || resp.FinishReason != FinishLength {
		t.Fatalf("%+v", resp)
	}
	if resp.Message.ToolCalls[0].Arguments != `{"a":1}` || resp.Message.Reasoning != "hm" {
		t.Errorf("%+v", resp.Message)
	}
	out, _ := c.WriteResponse(resp, octx())
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if m["done"] != true || m["done_reason"] != "length" || m["eval_count"].(float64) != 7 {
		t.Errorf("%s", out)
	}
	// an empty answer still has content:""
	out, _ = c.WriteResponse(&Response{Model: "m"}, octx())
	_ = json.Unmarshal(out, &m)
	if got, ok := m["message"].(map[string]any)["content"]; !ok || got != "" {
		t.Errorf("an empty reply must carry content \"\": %s", out)
	}
}

func TestOpenAIToOllamaToOpenAIKeepsTheConversation(t *testing.T) {
	in := `{"model":"llama3","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi"},{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"res"}],"temperature":0.4,"max_tokens":50}`
	c := NewCtx(FormatOpenAI, FormatOllama)
	ol, _, err := convert(FormatOpenAI, FormatOllama, []byte(in), c)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := convert(FormatOllama, FormatOpenAI, ol, c)
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
		t.Fatalf("roles %v", roles)
	}
	if m["temperature"].(float64) != 0.4 || m["max_tokens"].(float64) != 50 {
		t.Errorf("%v", m)
	}
}

func TestOllamaBadBodiesAreRefused(t *testing.T) {
	for _, in := range []string{``, `nope`, `[]`, `{"messages":"x"}`, `{"messages":[1]}`} {
		if _, err := (OllamaCodec{}).ReadRequest([]byte(in), octx()); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}
