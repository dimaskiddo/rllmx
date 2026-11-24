package gemini

import (
	"encoding/json"
	"rllmx/app/llm/translate"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

func googleCtx(provider string) *translate.Ctx {
	c := translate.NewCtx(translate.FormatOpenAI, translate.FormatCloudCode)
	c.Provider = provider
	c.Signature = "PLACEHOLDER"
	return c
}

func toolTurn() *translate.Request {
	temp := 0.4
	max := 200
	r := &translate.Request{
		Model: "m",
		Messages: []translate.Message{
			{Role: translate.RoleSystem, Content: translate.TextPart("You are helpful.")},
			{Role: translate.RoleUser, Content: translate.TextPart("What is the weather?")},
			{Role: translate.RoleAssistant, Content: translate.TextPart("Let me check."), ToolCalls: []translate.ToolCall{{ID: "call_1", Name: "weather", Arguments: `{"city":"Paris"}`}}},
			{Role: translate.RoleTool, ToolCallID: "call_1", Name: "weather", Content: translate.TextPart("sunny, 22C")},
			{Role: translate.RoleAssistant, Content: translate.TextPart("It is sunny.")},
			{Role: translate.RoleUser, Content: translate.TextPart("Thanks!")},
		},
		Tools:  []translate.Tool{{Name: "weather", Description: "get weather", Parameters: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)}},
		Params: translate.Params{Temperature: &temp, MaxTokens: &max},
	}
	r.Model = "gemini-3-pro-preview"
	return r
}

func TestAGeminiCLIBodyIsWrappedInTheEnvelopeWithSafetyOff(t *testing.T) {
	out, err := CloudCodeCodec{}.WriteRequest(toolTurn(), googleCtx("gemini-cli"))
	if err != nil {
		t.Fatal(err)
	}
	g := gjson.ParseBytes(out)
	if g.Get("userAgent").String() != "gemini-cli" || g.Get("model").String() != "gemini-3-pro-preview" || g.Get("project").String() != "" || g.Get("requestId").String() != "" {
		t.Fatalf("%s", out)
	}
	if g.Get("request.contents").Array()[0].Get("role").String() != "user" || len(g.Get("request.safetySettings").Array()) != 5 {
		t.Fatalf("%s", out)
	}
	if g.Get("request.safetySettings.0.threshold").String() != "OFF" {
		t.Fatalf("%s", out)
	}
	if g.Get("request.tools.0.functionDeclarations.0.name").String() != "weather" {
		t.Fatalf("%s", out)
	}
}

func TestAnAntigravityBodyHasNoSafetySettingsAndNoRequestType(t *testing.T) {
	out, err := CloudCodeCodec{}.WriteRequest(toolTurn(), googleCtx("antigravity"))
	if err != nil {
		t.Fatal(err)
	}
	g := gjson.ParseBytes(out)
	if g.Get("userAgent").String() != "antigravity" || g.Get("request.safetySettings").Exists() || g.Get("requestType").Exists() {
		t.Fatalf("%s", out)
	}
	if g.Get("request.toolConfig.functionCallingConfig.mode").String() != "VALIDATED" {
		t.Fatalf("tools without the validated mode: %s", out)
	}
}

func TestAntigravityRewritesCompetingBrandingInTheSystemPrompt(t *testing.T) {
	for in, want := range map[string]string{
		"You are a Claude agent, built on Anthropic's Claude Agent SDK. Be brief.": " Be brief.",
		"You are Hermes Agent, built by Nous Research. Help.":                      "You are an AI assistant. Help.",
		"x-anthropic-billing-header: cc=1;\nReal prompt":                           "Real prompt",
		"Use OpenCode, OPENCODE and opencode.":                                     "Use Antigravity, ANTIGRAVITY and antigravity.",
		"A plain prompt.":                                                          "A plain prompt.",
	} {
		if got := RewriteBranding(in); got != want {
			t.Errorf("%q\n got %q\nwant %q", in, got, want)
		}
	}
	r := toolTurn()
	r.Messages[0] = translate.Message{Role: translate.RoleSystem, Content: translate.TextPart("You are Hermes. Go.")}
	out, _ := CloudCodeCodec{}.WriteRequest(r, googleCtx("antigravity"))
	if got := gjson.GetBytes(out, "request.systemInstruction.parts.1.text").String(); got != "You are an AI assistant. Go." {
		t.Fatalf("%q", got)
	}
}

func TestAntigravityCapsOutputAndDropsAnotherVendorsThinkingFields(t *testing.T) {
	r := toolTurn()
	big := 200000
	r.Params.MaxTokens = &big
	r.Extra = map[string]json.RawMessage{"thinking": []byte(`{"type":"enabled"}`), "reasoning_effort": []byte(`"high"`), "output_config": []byte(`{}`)}
	out, _ := CloudCodeCodec{}.WriteRequest(r, googleCtx("antigravity"))
	g := gjson.ParseBytes(out)
	if g.Get("request.generationConfig.maxOutputTokens").Int() != antigravityMaxOutput {
		t.Fatalf("%s", out)
	}
	for _, k := range antigravityBlacklist {
		if g.Get("request." + k).Exists() {
			t.Errorf("%s was sent: %s", k, out)
		}
	}
}

func TestThePlaceholderSignatureGoesOnTheFirstCallOnly(t *testing.T) {
	r := &translate.Request{Model: "m", Messages: []translate.Message{
		{Role: translate.RoleUser, Content: translate.TextPart("go")},
		{Role: translate.RoleAssistant, ToolCalls: []translate.ToolCall{{ID: "a", Name: "f", Arguments: `{}`}, {ID: "b", Name: "g", Arguments: `{}`}, {ID: "c", Name: "h", Arguments: `{}`, Signature: "REAL"}}},
		{Role: translate.RoleTool, ToolCallID: "a", Name: "f", Content: translate.TextPart("1")},
		{Role: translate.RoleTool, ToolCallID: "b", Name: "g", Content: translate.TextPart("2")},
		{Role: translate.RoleTool, ToolCallID: "c", Name: "h", Content: translate.TextPart("3")},
	}}
	out, _ := CloudCodeCodec{}.WriteRequest(r, googleCtx("antigravity"))
	parts := gjson.GetBytes(out, "request.contents.1.parts").Array()
	if len(parts) != 3 || parts[0].Get("thoughtSignature").String() != "PLACEHOLDER" || parts[1].Get("thoughtSignature").Exists() || parts[2].Get("thoughtSignature").String() != "REAL" {
		t.Fatalf("%s", out)
	}
}

func TestACloudCodeAnswerIsReadWrappedOrBare(t *testing.T) {
	bare := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`
	for name, body := range map[string]string{"bare": bare, "wrapped": `{"response":` + bare + `,"traceId":"t"}`} {
		resp, err := CloudCodeCodec{}.ReadResponse([]byte(body), translate.NewCtx(translate.FormatCloudCode, translate.FormatOpenAI))
		if err != nil || resp.Message.Text() != "hi" || resp.Usage.Prompt != 5 {
			t.Errorf("%s: %+v %v", name, resp, err)
		}
	}
}

func TestACloudCodeStreamFrameIsReadThroughItsWrapper(t *testing.T) {
	r := CloudCodeCodec{}.NewStreamReader(translate.NewCtx(translate.FormatCloudCode, translate.FormatOpenAI))
	chunks := r.Read("", []byte(`{"response":{"candidates":[{"content":{"parts":[{"text":"yo"}]}}],"responseId":"r1"},"traceId":"t"}`))
	text := ""
	for _, c := range chunks {
		text += c.Text
	}
	if text != "yo" {
		t.Fatalf("%+v", chunks)
	}
}

func TestVertexTakesTheIdsOffAndUsesItsOwnSignature(t *testing.T) {
	c := translate.NewCtx(translate.FormatOpenAI, translate.FormatVertex)
	c.Signature = "VERTEX-SIG"
	r := &translate.Request{Model: "m", Messages: []translate.Message{
		{Role: translate.RoleUser, Content: translate.TextPart("go")},
		{Role: translate.RoleAssistant, ToolCalls: []translate.ToolCall{{ID: "a", Name: "f", Arguments: `{}`, Signature: "CLIENT-SIG"}}},
		{Role: translate.RoleTool, ToolCallID: "a", Name: "f", Content: translate.TextPart("1")},
	}}
	out, err := VertexCodec{}.WriteRequest(r, c)
	if err != nil {
		t.Fatal(err)
	}
	g := gjson.ParseBytes(out)
	if g.Get("contents.1.parts.0.functionCall.id").Exists() || g.Get("contents.2.parts.0.functionResponse.id").Exists() {
		t.Fatalf("an id was sent: %s", out)
	}
	if g.Get("contents.1.parts.0.thoughtSignature").String() != "VERTEX-SIG" || g.Get("safetySettings").Exists() {
		t.Fatalf("%s", out)
	}
	if strings.Contains(string(out), "CLIENT-SIG") {
		t.Fatalf("a foreign signature was kept: %s", out)
	}
}

func TestEveryGoogleFormatIsInTheFamilyAndNothingElseIs(t *testing.T) {
	for _, f := range []string{translate.FormatGemini, translate.FormatCloudCode, translate.FormatVertex} {
		if !translate.GeminiFamily(f) {
			t.Errorf("%s", f)
		}
	}
	for _, f := range []string{translate.FormatOpenAI, translate.FormatClaude, translate.FormatResponses, translate.FormatOllama, ""} {
		if translate.GeminiFamily(f) {
			t.Errorf("%s", f)
		}
	}
	for _, f := range []string{upstream.FormatCloudCode, upstream.FormatVertex} {
		if _, ok := translate.Lookup(f); !ok {
			t.Errorf("the registry names %s but no codec is registered", f)
		}
	}
}

func FuzzCloudCodeEnvelope(f *testing.F) {
	for _, s := range []string{`{}`, `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, `[`, `{"systemInstruction":{"parts":[{"text":"OpenCode"}]}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := GeminiCodec{}.ReadRequest(body, translate.NewCtx(translate.FormatGemini, translate.FormatOpenAI))
		if err != nil {
			return
		}
		for _, p := range []string{"gemini-cli", "antigravity"} {
			out, err := CloudCodeCodec{}.WriteRequest(req, googleCtx(p))
			if err != nil || !gjson.ValidBytes(out) || !gjson.GetBytes(out, "request").IsObject() {
				t.Fatalf("%s: %v %s", p, err, out)
			}
		}
	})
}

// Whole lines of a Hermes prompt each earn a fake quota error from Antigravity,
// so they go, and the generic identity rule runs after them so it cannot eat
// the start of a line they would have matched.
func TestAntigravityDropsTheHermesLinesThatTriggerAFakeQuotaError(t *testing.T) {
	in := "You are Hermes Agent, built by Nous Research. Be direct: answer first.\n" +
		"Keep this line.\n" +
		"<!-- End Hermes runtime environment -->\n" +
		"# Google model operational directives\n" +
		"If a tool, install, or network call fails and blocks the real path, stop.\n" +
		"When the user asks you to build, run, or verify something, the deliverable is a working artifact.\n" +
		"For help with Hermes Agent itself, read the docs.\n" +
		"Mid-turn, the user can steer you: Hermes delivers their message as a note.\n" +
		"Keep this one too."
	if got, want := RewriteBranding(in), "You are an AI assistant.\nKeep this line.\nKeep this one too."; got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

func TestAntigravityRenamesTheOpenClawHeader(t *testing.T) {
	if got := RewriteBranding("You are a personal assistant running inside OpenClaw. Help."); got != "You are a personal assistant running inside Antigravity. Help." {
		t.Fatalf("%q", got)
	}
}

// Antigravity answers a prompt that does not start with its own identity as a
// foreign client. The line is added once, and only when the prompt has none.
func TestAntigravityLeadsTheSystemPromptWithItsOwnIdentityOnce(t *testing.T) {
	r := toolTurn()
	r.Messages[0] = translate.Message{Role: translate.RoleSystem, Content: translate.TextPart("Be brief.")}
	out, _ := CloudCodeCodec{}.WriteRequest(r, googleCtx("antigravity"))
	parts := gjson.GetBytes(out, "request.systemInstruction.parts").Array()
	if len(parts) != 2 || !strings.HasPrefix(parts[0].Get("text").String(), "You are Antigravity Agent, a powerful agentic AI coding assistant") || parts[1].Get("text").String() != "Be brief." {
		t.Fatalf("%s", gjson.GetBytes(out, "request.systemInstruction").Raw)
	}
	r.Messages[0] = translate.Message{Role: translate.RoleSystem, Content: translate.TextPart("You are Antigravity Agent. Custom.")}
	out, _ = CloudCodeCodec{}.WriteRequest(r, googleCtx("antigravity"))
	if n := len(gjson.GetBytes(out, "request.systemInstruction.parts").Array()); n != 1 {
		t.Fatalf("the identity was added to a prompt that already had one: %d parts", n)
	}
	if other, _ := (CloudCodeCodec{}).WriteRequest(toolTurn(), googleCtx("gemini-cli")); strings.Contains(string(other), "Antigravity Agent") {
		t.Fatal("another provider was given Antigravity's identity")
	}
}

func TestAntigravityKeepsALongDecodeModelsBudgetAndStillCutsTheOthers(t *testing.T) {
	ask := func(long bool, n int) int64 {
		r := toolTurn()
		r.Params.MaxTokens = &n
		c := googleCtx("antigravity")
		c.Caps.LongDecode = long
		out, _ := CloudCodeCodec{}.WriteRequest(r, c)
		return gjson.GetBytes(out, "request.generationConfig.maxOutputTokens").Int()
	}
	if got := ask(true, 500000); got != 500000 {
		t.Errorf("a long decode model was cut to %d", got)
	}
	if got := ask(false, 500000); got != antigravityMaxOutput {
		t.Errorf("another model was not cut: %d", got)
	}
}
