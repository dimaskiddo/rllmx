package openai

import (
	"bytes"
	"encoding/json"
	"github.com/tidwall/gjson"
	"rllmx/app/llm/translate"
	"strings"
	"testing"
)

func ctx() *translate.Ctx { return translate.NewCtx(translate.FormatOpenAI, translate.FormatOpenAI) }

func mustRead(t *testing.T, c translate.Codec, body string) *translate.Request {
	t.Helper()
	r, err := c.ReadRequest([]byte(body), ctx())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return r
}

func canon(t *testing.T, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not json: %v: %s", err, raw)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestDetectByEndpointAlone(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/chat/completions": translate.FormatOpenAI, "/v1/messages": translate.FormatClaude, "/v1/messages/count_tokens": translate.FormatClaude,
		"/v1/responses": translate.FormatResponses, "/v1/responses/compact": translate.FormatResponses, "/v1/api/chat": translate.FormatOllama,
		"/v1beta/models/gemini-2.5-pro:generateContent": translate.FormatGemini,
	} {
		if got, err := translate.Detect(path); err != nil || got != want {
			t.Errorf("%s: %q %v, want %q", path, got, err, want)
		}
	}
	for _, path := range []string{"/", "/v1", "/v1/embeddings", "/api/providers", "/v1/chat/completions/x"} {
		if _, err := translate.Detect(path); err == nil {
			t.Errorf("%s was given a format", path)
		}
	}
}

func TestGeminiActionTakesProviderAndModel(t *testing.T) {
	m, a, ok := translate.GeminiAction("/v1beta/models/openai/gpt-4o:streamGenerateContent")
	if !ok || m != "openai/gpt-4o" || a != "streamGenerateContent" {
		t.Fatalf("%q %q %v", m, a, ok)
	}
	if m, a, ok := translate.GeminiAction("/v1beta/models/x:countTokens"); !ok || m != "x" || a != "countTokens" {
		t.Fatalf("%q %q %v", m, a, ok)
	}
}

func FuzzGeminiAction(f *testing.F) {
	for _, s := range []string{"/v1beta/models/a/b:generateContent", "/v1beta/models/../x:y", "/v1beta/models/a%2Fb:c", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		m, _, ok := translate.GeminiAction(path)
		if !ok {
			return
		}
		if strings.Contains(m, "..") || strings.ContainsAny(m, ":?%") || strings.Count(m, "/") > 1 {
			t.Fatalf("unsafe model %q from %q", m, path)
		}
	})
}

func TestGeminiActionRejectsACraftedModelSegment(t *testing.T) {
	if m, a, ok := translate.GeminiAction("/v1beta/models/gemini-2.5-pro:streamGenerateContent"); !ok || m != "gemini-2.5-pro" || a != "streamGenerateContent" {
		t.Fatalf("%q %q %v", m, a, ok)
	}
	for _, p := range []string{
		"/v1beta/models/../x:generateContent", "/v1beta/models/a/b/c:generateContent", "/v1beta/models/a/../b:generateContent", "/v1beta/models/a//b:generateContent", "/v1beta/models/a:b:generateContent", "/v1beta/models/:generateContent",
		"/v1beta/models/a b:generateContent", "/v1beta/models/gemini", "/v1beta/models/gemini:", "/v1/models/gemini:generateContent",
		"/v1beta/models/a%2Fb:generateContent", "/v1beta/models/a?x=1:generateContent",
	} {
		if _, _, ok := translate.GeminiAction(p); ok {
			t.Errorf("%q was accepted", p)
		}
	}
}

func TestTheOpenAIRoundTripKeepsEveryModelledFieldAndTheUnknownOnes(t *testing.T) {
	in := `{"model":"gpt-4o","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"https://x.example/a.png","detail":"low"}}]},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{\"a\":1}"}}]},{"role":"tool","tool_call_id":"c1","content":"ok"}],"tools":[{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object","properties":{"a":{"type":"number"}}}}}],"tool_choice":"auto","max_tokens":50,"temperature":0.2,"top_p":0.9,"seed":7,"stop":["x"],"user":"u1","logit_bias":{"50256":-100},"service_tier":"flex"}`
	c := OpenAICodec{}
	req := mustRead(t, c, in)
	out, err := c.WriteRequest(req, ctx())
	if err != nil {
		t.Fatal(err)
	}
	var a, b map[string]any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	for _, k := range []string{"model", "tools", "tool_choice", "max_tokens", "temperature", "top_p", "seed", "stop", "user", "logit_bias", "service_tier"} {
		x, _ := json.Marshal(a[k])
		y, _ := json.Marshal(b[k])
		if string(x) != string(y) {
			t.Errorf("%s: %s != %s", k, x, y)
		}
	}
	msgs := b["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("%d messages", len(msgs))
	}
	call := msgs[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)
	if call["id"] != "c1" || call["function"].(map[string]any)["arguments"] != `{"a":1}` {
		t.Errorf("tool call: %v", call)
	}
	if msgs[3].(map[string]any)["tool_call_id"] != "c1" {
		t.Errorf("tool result: %v", msgs[3])
	}
}

func TestTheDeveloperRoleBecomesSystemAndBlankMessagesGo(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"developer","content":"rules"},{"role":"user","content":"   "},{"role":"user","content":""},{"role":"user","content":"hi"}]}`
	c := OpenAICodec{}
	out, _ := c.WriteRequest(mustRead(t, c, in), ctx())
	got := canon(t, out)
	if strings.Contains(got, "developer") {
		t.Errorf("the developer role survived: %s", got)
	}
	if strings.Count(got, `"role":"user"`) != 1 || !strings.Contains(got, `"role":"system"`) {
		t.Errorf("blank messages or the role: %s", got)
	}
}

func TestForeignBlocksAreDroppedFromOpenAIContent(t *testing.T) {
	in := `{"model":"m","messages":[{"role":"user","content":[{"type":"thinking","thinking":"x"},{"type":"tool_use","id":"t","name":"n"},{"type":"text","text":"kept"},{"type":"redacted_thinking","data":"d"}]}]}`
	c := OpenAICodec{}
	req := mustRead(t, c, in)
	if len(req.Messages[0].Content) != 1 || req.Messages[0].Content[0].Text != "kept" {
		t.Fatalf("%+v", req.Messages[0].Content)
	}
}

func TestAnEmptyToolsArrayIsNotSent(t *testing.T) {
	c := OpenAICodec{}
	out, _ := c.WriteRequest(mustRead(t, c, `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[]}`), ctx())
	if strings.Contains(string(out), `"tools"`) {
		t.Fatalf("%s", out)
	}
}

func TestStreamAsksForUsage(t *testing.T) {
	c := OpenAICodec{}
	out, _ := c.WriteRequest(mustRead(t, c, `{"model":"m","messages":[{"role":"user","content":"x"}],"stream":true}`), ctx())
	if !strings.Contains(string(out), `"include_usage":true`) {
		t.Fatalf("%s", out)
	}
}

func TestEncodingIsDeterministic(t *testing.T) {
	c := OpenAICodec{}
	in := `{"model":"m","messages":[{"role":"user","content":"x"}],"zeta":1,"alpha":{"b":1,"a":2},"mid":[3,2,1]}`
	first, _ := c.WriteRequest(mustRead(t, c, in), ctx())
	for range 20 {
		again, _ := c.WriteRequest(mustRead(t, c, in), ctx())
		if !bytes.Equal(first, again) {
			t.Fatalf("two encodings of one request differ:\n%s\n%s", first, again)
		}
	}
}

func TestReasoningEffortMapsToThinking(t *testing.T) {
	c := OpenAICodec{}
	for effort, want := range map[string]translate.Thinking{
		"high": {Mode: "level", Level: "high"}, "none": {Mode: "none"}, "auto": {Mode: "auto"}, "MEDIUM": {Mode: "level", Level: "medium"},
	} {
		if got := mustRead(t, c, `{"model":"m","reasoning_effort":"`+effort+`","messages":[]}`).Thinking; got != want {
			t.Errorf("%s: %+v, want %+v", effort, got, want)
		}
	}
	if mustRead(t, c, `{"model":"m","messages":[]}`).Thinking.Set() {
		t.Error("nothing asked, thinking translate.Set")
	}
}

func TestMaxCompletionTokensWinsOverMaxTokens(t *testing.T) {
	c := OpenAICodec{}
	r := mustRead(t, c, `{"model":"m","messages":[],"max_tokens":10,"max_completion_tokens":99}`)
	if r.Params.MaxTokens == nil || *r.Params.MaxTokens != 99 {
		t.Fatalf("%v", r.Params.MaxTokens)
	}
}

func TestToolArgumentsGivenAsAnObjectAreKeptAsText(t *testing.T) {
	c := OpenAICodec{}
	r := mustRead(t, c, `{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"1","function":{"name":"f","arguments":{"a":1}}}]}]}`)
	if got := r.Messages[0].ToolCalls[0].Arguments; got != `{"a":1}` {
		t.Fatalf("%q", got)
	}
}

func TestBadBodiesAreRefused(t *testing.T) {
	c := OpenAICodec{}
	for _, in := range []string{``, `nope`, `[]`, `"x"`, `{"messages":"x"}`, `{"messages":[1]}`, `{"messages":[[]]}`} {
		if _, err := c.ReadRequest([]byte(in), ctx()); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestTheResponseRoundTrip(t *testing.T) {
	in := `{"id":"chatcmpl-1","object":"chat.completion","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning_content":"thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":2}},"system_fingerprint":"fp_1"}`
	c := OpenAICodec{}
	resp, err := c.ReadResponse([]byte(in), ctx())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "hello" || resp.Message.Reasoning != "thinking" || resp.FinishReason != "length" {
		t.Fatalf("%+v", resp)
	}
	if resp.Usage.Prompt != 10 || resp.Usage.Cached != 4 || resp.Usage.Reasoning != 2 || resp.Usage.Completion != 5 {
		t.Fatalf("%+v", resp.Usage)
	}
	out, _ := c.WriteResponse(resp, ctx())
	var b map[string]any
	_ = json.Unmarshal(out, &b)
	if b["system_fingerprint"] != "fp_1" || b["id"] != "chatcmpl-1" || b["object"] != "chat.completion" {
		t.Errorf("%s", out)
	}
	if b["usage"].(map[string]any)["total_tokens"].(float64) != 15 {
		t.Errorf("usage %v", b["usage"])
	}
}

func TestUsageInvariants(t *testing.T) {
	for _, u := range []translate.Usage{
		{Prompt: 10, Cached: 99, CacheWrite: 99, Completion: 5, Reasoning: 99},
		{Prompt: -5, Completion: -5, Cached: -5, Reasoning: -5},
		{Prompt: 10, Cached: 6, CacheWrite: 9, Completion: 3, Reasoning: 3},
		{},
	} {
		n := translate.NormalizeUsage(u)
		if n.Prompt < 0 || n.Completion < 0 || n.Cached < 0 || n.CacheWrite < 0 || n.Reasoning < 0 {
			t.Errorf("%+v: negative %+v", u, n)
		}
		if n.Cached+n.CacheWrite > n.Prompt {
			t.Errorf("%+v: cached+write exceeds prompt %+v", u, n)
		}
		if n.Reasoning > n.Completion {
			t.Errorf("%+v: reasoning exceeds completion %+v", u, n)
		}
	}
}

func TestNeedsReadTheCurrentTurnOnly(t *testing.T) {
	img := translate.Message{Role: translate.RoleUser, Content: []translate.Part{{Type: translate.PartImage, URL: "u"}}}
	txt := translate.Message{Role: translate.RoleUser, Content: translate.TextPart("now plain")}
	asst := translate.Message{Role: translate.RoleAssistant, Content: translate.TextPart("ok")}
	if n := translate.DetectNeeds(&translate.Request{Messages: []translate.Message{img, asst, txt}}); n.Any() {
		t.Errorf("an old image counted: %+v", n)
	}
	if n := translate.DetectNeeds(&translate.Request{Messages: []translate.Message{txt, asst, img}}); !n.Vision {
		t.Errorf("the current image was missed: %+v", n)
	}
	// every message after the last assistant turn counts
	if n := translate.DetectNeeds(&translate.Request{Messages: []translate.Message{asst, img, txt}}); !n.Vision {
		t.Errorf("an image earlier in the current turn: %+v", n)
	}
	n := translate.DetectNeeds(&translate.Request{Messages: []translate.Message{{Role: translate.RoleUser, Content: []translate.Part{
		{Type: translate.PartAudio}, {Type: translate.PartFile, MediaType: "application/pdf"}, {Type: translate.PartFile, MediaType: "video/mp4"},
	}}}})
	if !n.Audio || !n.PDF || !n.Video || n.Vision {
		t.Errorf("%+v", n)
	}
	if translate.DetectNeeds(&translate.Request{}).Any() {
		t.Error("an empty request needs something")
	}
}

func TestCodecRegistryRefusesADuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering a second codec of one name did not panic")
		}
	}()
	translate.Register(OpenAICodec{})
}

func TestConvertGoesThroughTheHub(t *testing.T) {
	out, req, err := translate.Convert(translate.FormatOpenAI, translate.FormatOpenAI, []byte(`{"model":"m","messages":[{"role":"user","content":"x"}]}`), ctx())
	if err != nil || req == nil || !strings.Contains(string(out), `"model":"m"`) {
		t.Fatalf("%s %v", out, err)
	}
	if _, _, err := translate.Convert("nope", translate.FormatOpenAI, nil, ctx()); err == nil {
		t.Error("an unknown source format converted")
	}
	if _, _, err := translate.Convert(translate.FormatOpenAI, "nope", []byte(`{}`), ctx()); err == nil {
		t.Error("an unknown target format converted")
	}
}

func TestGeminiActionRefusesDotDotInsideASegment(t *testing.T) {
	if _, _, ok := translate.GeminiAction("/v1beta/models/..0:generateContent"); ok {
		t.Fatal("a segment with .. was accepted")
	}
}

func TestReasoningIsReadFromTheAliasOnlyWhenItIsText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"role":"assistant","reasoning_content":"a","reasoning":"b"}`, "a"},
		{`{"role":"assistant","reasoning":"b"}`, "b"},
		{`{"role":"assistant","reasoning":{"summary":"s"}}`, ""},
	} {
		if got := ReadOpenAIMessage(gjson.Parse(c.in)).Reasoning; got != c.want {
			t.Errorf("%s: %q", c.in, got)
		}
	}
}

func TestAToolMessageCarriesItsErrorState(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{`{"role":"tool","tool_call_id":"t","content":"x","is_error":true}`, true},
		{`{"role":"tool","tool_call_id":"t","content":"x","status":"error"}`, true},
		{`{"role":"tool","tool_call_id":"t","content":"x","is_error":false}`, false},
		{`{"role":"tool","tool_call_id":"t","content":"x"}`, false},
	} {
		if got := ReadOpenAIMessage(gjson.Parse(c.in)).Error; got != c.want {
			t.Errorf("%s: %v", c.in, got)
		}
	}
}

// An agent client may put the picture beside the content instead of inside it:
// Ollama's `images`, the AI SDK's attachments, and an `image` block each have to
// reach the hub, because a picture the reader ignores is one no model sees.
func TestAnImageBesideTheContentIsRead(t *testing.T) {
	body := `{"model":"m","messages":[
		{"role":"user","content":"look","images":["data:image/png;base64,AAAA"]},
		{"role":"user","content":"bare","images":["BBBB"]},
		{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image","image":{"url":"https://e/x.png"}}]},
		{"role":"user","content":"see","experimental_attachments":[{"contentType":"image/jpeg","url":"https://e/y.jpg"},{"contentType":"application/pdf","url":"https://e/z.pdf"}]},
		{"role":"user","content":"see","attachments":[{"contentType":"image/png","data":"CCCC"}]}]}`
	r := mustRead(t, OpenAICodec{}, body)
	if len(r.Messages) != 5 {
		t.Fatalf("%d messages", len(r.Messages))
	}
	want := []translate.Part{
		{Type: translate.PartImage, URL: "data:image/png;base64,AAAA"},
		{Type: translate.PartImage, Data: "BBBB", MediaType: "image/png"},
		{Type: translate.PartImage, URL: "https://e/x.png"},
		{Type: translate.PartImage, URL: "https://e/y.jpg"},
		{Type: translate.PartImage, URL: "data:image/png;base64,CCCC"},
	}
	for i, m := range r.Messages {
		if len(m.Content) != 2 {
			t.Fatalf("message %d kept %d parts: %+v", i, len(m.Content), m.Content)
		}
		got := m.Content[1]
		if got.Type != want[i].Type || got.URL != want[i].URL || got.Data != want[i].Data || got.MediaType != want[i].MediaType {
			t.Errorf("message %d: %+v, want %+v", i, got, want[i])
		}
	}
	if !translate.DetectNeeds(r).Vision {
		t.Error("an image beside the content is not a need")
	}
}
