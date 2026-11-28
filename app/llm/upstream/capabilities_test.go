package upstream

import (
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func caps(t *testing.T) *Capabilities {
	t.Helper()
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTheEmbeddedCapabilityTablesLoad(t *testing.T) {
	c := caps(t)
	if c.ClaudeSignature() == "" || c.GeminiSignature() == "" {
		t.Fatal("a placeholder signature is empty")
	}
	if c.Budget("high") <= c.Budget("low") || c.Budget("low") <= 0 {
		t.Fatalf("budgets %d %d", c.Budget("low"), c.Budget("high"))
	}
	if c.Budget("no-such-level") != 0 {
		t.Fatal("an unknown level has a budget")
	}
}

func TestEverySeedModelResolvesToSomethingUseful(t *testing.T) {
	c := caps(t)
	reg, _ := Default()
	for _, e := range reg.All() {
		for _, m := range e.Models {
			cp := c.For(e.ID, m.ID)
			if cp.MaxOutput <= 0 || cp.ContextWindow <= 0 {
				t.Errorf("%s/%s: %+v", e.ID, m.ID, cp)
			}
		}
	}
}

func TestResolutionOrder(t *testing.T) {
	c := caps(t)
	// provider row beats model row and glob
	if got := c.For("github", "claude-opus-4.8"); got.MaxOutput != 32000 {
		t.Errorf("provider row: %+v", got)
	}
	// the same name elsewhere takes the glob
	if got := c.For("anthropic", "claude-opus-4.8"); got.MaxOutput != 64000 {
		t.Errorf("glob: %+v", got)
	}
	// exact model beats glob
	if got := c.For("openai", "gpt-4o"); got.MaxOutput != 16384 || !got.Vision {
		t.Errorf("model row: %+v", got)
	}
	// a provider alias shares the provider's table
	if got := c.For("gh", "claude-opus-4.8"); got.MaxOutput != 32000 {
		t.Errorf("alias: %+v", got)
	}
	// the name after the last slash is tried
	if got := c.For("openrouter", "anthropic/claude-sonnet-5"); !got.Reasoning || !got.Vision {
		t.Errorf("vendor prefix: %+v", got)
	}
	// unknown model: the defaults
	got := c.For("openai", "totally-unknown")
	if !got.Tools || got.Vision || got.Reasoning || got.ContextWindow != 200000 || got.MaxOutput != 64000 {
		t.Errorf("defaults: %+v", got)
	}
	if got.VisionSet {
		t.Error("a model the tables never mention claims a vision answer")
	}
	if !c.For("openai", "gpt-4o").VisionSet {
		t.Error("a model row is not known about vision")
	}
}

func TestVisionFromTheNameNeverOverridesTheNotVisionList(t *testing.T) {
	c := caps(t)
	for model, want := range map[string]bool{
		"qwen3-vl-plus": true, "glm-4.6v": true, "some-model-vision": true, "llava-13b": true,
		"qwen3-vl-embed": false, "flux-vision": false, "whisper-vision": false, "plain-chat": false, "text-embedding-vl": false,
	} {
		if got := c.For("openai", model).Vision; got != want {
			t.Errorf("%s: vision %v, want %v", model, got, want)
		}
	}
}

func TestAProviderRowIsReturnedAsWrittenEvenWithAVisionLookingName(t *testing.T) {
	reg, _ := Default()
	c, err := NewCapabilities(
		[]byte(`{"providers":{"p":{"my-vision-model":{"contextWindow":5}}}}`),
		[]byte(thinkingJSON), []byte(visionJSON), []byte(`{"rules":[]}`), reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.For("p", "my-vision-model"); got.Vision {
		t.Fatal("a provider row was changed by the name rule")
	}
}

func TestThinkingLevels(t *testing.T) {
	c := caps(t)
	if c.Levels("openai", "gpt-4o", nil) != nil {
		t.Error("a model that does not reason has levels")
	}
	// the declared levels win
	if got := c.Levels("anthropic", "claude-opus-5", []string{"low", "high"}); !slices.Equal(got, []string{"low", "high"}) {
		t.Errorf("declared: %v", got)
	}
	// an override beats the format
	if got := c.Levels("gemini", "gemini-4-argon", nil); !slices.Equal(got, []string{"low", "medium", "high"}) {
		t.Errorf("gemini 4: %v", got)
	}
	if got := c.Levels("anthropic", "claude-opus-4.6", nil); slices.Contains(got, "xhigh") || !slices.Contains(got, "max") {
		t.Errorf("claude 4.6: %v", got)
	}
	// a provider-qualified rule applies to that provider only
	if got := c.Levels("codex", "gpt-6-sol", nil); !slices.Contains(got, "max") {
		t.Errorf("codex gpt-6: %v", got)
	}
	// gpt-5.6 has a rule of its own that only codex gets; another provider serving
	// the same model name must fall through to the format's set.
	if got := c.Levels("codex", "gpt-5.6-sol", nil); !slices.Contains(got, "ultra") {
		t.Errorf("codex gpt-5.6 lost its qualified rule: %v", got)
	}
	if got := c.Levels("openai", "gpt-5.6-sol", nil); slices.Contains(got, "ultra") {
		t.Errorf("a codex-only level leaked to openai: %v", got)
	}
	// the format's set, with none removed for a model that cannot disable
	got := c.Levels("openai", "o3", nil)
	if slices.Contains(got, "none") || !slices.Contains(got, "high") {
		t.Errorf("o3: %v", got)
	}
	if got := c.Levels("deepseek", "deepseek-v4-pro", nil); !slices.Equal(got, []string{"none", "high", "max"}) {
		t.Errorf("deepseek: %v", got)
	}
}

func TestParamRulesApplyInFileOrderAndHonorProviderAndExclusion(t *testing.T) {
	c := caps(t)
	if r := c.ParamRules("openai", "gpt-4o"); len(r) != 0 {
		t.Errorf("a plain model got rules: %+v", r)
	}
	r := c.ParamRules("openai", "gpt-5.6-sol")
	if len(r) != 1 || !slices.Contains(r[0].Drop, "temperature") {
		t.Errorf("gpt-5: %+v", r)
	}
	if r := c.ParamRules("anthropic", "claude-opus-5"); len(r) != 1 || r[0].Drop[0] != "temperature" {
		t.Errorf("claude opus: %+v", r)
	}
	if r := c.ParamRules("deepseek", "deepseek-reasoner"); len(r) != 1 {
		t.Errorf("deepseek reasoner: %+v", r)
	}
	if r := c.ParamRules("openai", "deepseek-reasoner"); len(r) != 0 {
		t.Errorf("a provider-qualified rule leaked: %+v", r)
	}
	if r := c.ParamRules("github", "anything"); len(r) != 1 || !r[0].FlattenContent {
		t.Errorf("github: %+v", r)
	}
}

const thinkingJSON = `{"base":["low"],"formats":{},"rules":[],"budgets":{},"claudeSignature":"a","geminiSignature":"b","geminiCliSignature":"c","antigravitySignature":"d","vertexSignature":"e"}`
const visionJSON = `{"not":["embed"],"name":["vision"]}`

func TestTheCapabilityLoaderRefusesBadTables(t *testing.T) {
	reg, _ := Default()
	good := map[string]string{"cap": `{}`, "think": thinkingJSON, "vision": visionJSON, "params": `{"rules":[]}`}
	build := func(over map[string]string) error {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		_, err := NewCapabilities([]byte(m["cap"]), []byte(m["think"]), []byte(m["vision"]), []byte(m["params"]), reg)
		return err
	}
	if err := build(nil); err != nil {
		t.Fatalf("the good tables fail: %v", err)
	}
	cases := map[string]struct {
		over map[string]string
		want string
	}{
		"unknown field":           {map[string]string{"cap": `{"models":{"m":{"vison":true}}}`}, "unknown field"},
		"negative limit":          {map[string]string{"cap": `{"models":{"m":{"maxOutput":-1}}}`}, "negative"},
		"inverted range":          {map[string]string{"cap": `{"models":{"m":{"thinkingMin":9,"thinkingMax":1}}}`}, "inverted"},
		"empty pattern":           {map[string]string{"cap": `{"patterns":[{"pattern":"","set":{}}]}`}, "empty"},
		"pattern set unknown key": {map[string]string{"cap": `{"patterns":[{"pattern":"a*","set":{"x":1}}]}`}, "unknown field"},
		"thinking rule no levels": {map[string]string{"think": `{"base":["a"],"rules":[{"pattern":"a"}],"claudeSignature":"a","geminiSignature":"b","geminiCliSignature":"c","antigravitySignature":"d","vertexSignature":"e"}`}, "levels"},
		"budget out of range":     {map[string]string{"think": `{"base":["a"],"budgets":{"x":-5},"claudeSignature":"a","geminiSignature":"b","geminiCliSignature":"c","antigravitySignature":"d","vertexSignature":"e"}`}, "range"},
		"no placeholder":          {map[string]string{"think": `{"base":["a"],"claudeSignature":"","geminiSignature":"b"}`}, "signatures"},
		"no google signature":     {map[string]string{"think": `{"base":["a"],"claudeSignature":"a","geminiSignature":"b","geminiCliSignature":"c","antigravitySignature":"","vertexSignature":"e"}`}, "Antigravity"},
		"vision no patterns":      {map[string]string{"vision": `{"not":[],"name":["v"]}`}, "no patterns"},
		"vision bad regexp":       {map[string]string{"vision": `{"not":["("],"name":["v"]}`}, "compile"},
		"param rule does nothing": {map[string]string{"params": `{"rules":[{"pattern":"a*"}]}`}, "does nothing"},
		"param negative cap":      {map[string]string{"params": `{"rules":[{"pattern":"a*","capOutput":-1}]}`}, "negative"},
		"exclude without pattern": {map[string]string{"params": `{"rules":[{"exclude":"a*","drop":["x"]}]}`}, "exclusion"},
		"credential-shaped field": {map[string]string{"params": `{"rules":[{"drop":["api_key"]}]}`}, "credential"},
		"two values":              {map[string]string{"cap": `{}{}`}, "more than one"},
		"not json":                {map[string]string{"params": `[`}, "valid JSON"},
	}
	for name, c := range cases {
		err := build(c.over)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

func TestACapabilityErrorRepeatsNoTableContent(t *testing.T) {
	reg, _ := Default()
	secret := "sk-secret-table-value"
	_, err := NewCapabilities([]byte(`{"models":{"m":{"`+secret+`":1}}}`), []byte(thinkingJSON), []byte(visionJSON), []byte(`{"rules":[]}`), reg)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("%v", err)
	}
}

// What an operator declares for a custom model reaches the lookup, under the
// alias the dashboard stores or the id a request resolves to, and a declared
// flag wins both ways.
func TestADeclaredCustomModelCarriesItsCapabilities(t *testing.T) {
	c := caps(t)
	const provider, model = "my-endpoint", "my-model"
	if c.For(provider, model).Vision {
		t.Fatal("the fixture is already a vision model")
	}
	on, off := true, false
	c.SetCustom(map[string]Declared{
		provider + "/" + model: {Vision: &on, Reasoning: &on, Audio: &on},
		"other/model":          {Vision: &on},
	})
	got := c.For(provider, model)
	if !got.Vision || !got.Reasoning || !got.AudioInput {
		t.Fatalf("%+v", got)
	}
	// A declared audio off wins over a table that says the model reads sound.
	if !c.For("gemini", "gemini-2.5-pro").AudioInput {
		t.Fatal("the fixture does not read audio")
	}
	c.SetCustom(map[string]Declared{"gemini/gemini-2.5-pro": {Audio: &off}})
	if c.For("gemini", "gemini-2.5-pro").AudioInput {
		t.Fatal("a declared audio off was ignored")
	}
	// The learned-vision path declares one flag and must keep the others.
	c.SetCustom(map[string]Declared{provider + "/" + model: {Audio: &on}})
	c.Declare(provider, model, Declared{Vision: &on})
	if !c.For(provider, model).AudioInput {
		t.Fatal("a declaration erased the audio flag")
	}
	if c.For(provider, "another-model").Vision {
		t.Fatal("another model took the declaration")
	}
	// A declared "off" wins over a table that says otherwise.
	if !c.For("openai", "gpt-4o").Vision {
		t.Fatal("the fixture is not a vision model")
	}
	c.SetCustom(map[string]Declared{"openai/gpt-4o": {Vision: &off}})
	if c.For("openai", "gpt-4o").Vision {
		t.Fatal("a declared off was ignored")
	}
	// An alias the dashboard stored reaches the id a request names.
	c.SetCustom(map[string]Declared{"cc/my-custom": {Vision: &on}})
	if !c.For("cc", "my-custom").Vision || !c.For("claude", "my-custom").Vision {
		t.Fatal("a declaration stored under the alias was lost")
	}
}

func TestALearnedFlagKeepsWhatTheOperatorStoredUnderTheAlias(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	c.SetCustom(map[string]Declared{"cc/claude-custom": {Reasoning: &on, Audio: &on}})
	c.Declare("claude", "claude-custom", Declared{Vision: &on})
	got := c.For("claude", "claude-custom")
	if !got.Vision || !got.Reasoning || !got.AudioInput {
		t.Fatalf("a learned vision flag hid the stored ones: %+v", got)
	}
}

// Neon and Hydrogen take a fourth level above high, and Antigravity's Claude 5.5
// takes max, so the picker offers them and a request for one is not turned into
// something else. Argon and Carbon stay at three.
func TestTheNewAntigravityTiersOfferTheirOwnLevels(t *testing.T) {
	c := caps(t)
	for model, want := range map[string][]string{
		"gemini-4-neon-high":     {"low", "medium", "high", "xhigh"},
		"gemini-4-hydrogen":      {"low", "medium", "high", "xhigh"},
		"gemini-4-carbon":        {"low", "medium", "high"},
		"claude-opus-5-5-max":    {"low", "medium", "high", "max"},
		"claude-sonnet-5-5-high": {"low", "medium", "high", "max"},
	} {
		if got := c.Levels("antigravity", model, nil); !slices.Equal(got, want) {
			t.Errorf("%s offers %v, want %v", model, got, want)
		}
	}
	if got := c.Levels("anthropic", "claude-opus-5-5", nil); slices.Equal(got, []string{"low", "medium", "high", "max"}) {
		t.Errorf("the Antigravity-only rule reached the Anthropic API: %v", got)
	}
}

func TestKimiK31IsAMillionTokenReasoningModelThatCannotSwitchThinkingOff(t *testing.T) {
	c := caps(t)
	for _, model := range []string{"k3.1-pro", "k3.1-pro-highspeed"} {
		cp := c.For("kimi", model)
		if !cp.Reasoning || cp.ThinkingCanOff || cp.ThinkingFormat != "kimi" || cp.ContextWindow != 1048576 || cp.MaxOutput != 131072 || !cp.Vision {
			t.Errorf("%s: %+v", model, cp)
		}
	}
}

func TestTheNewCatalogRowsCarryTheirLimits(t *testing.T) {
	c := caps(t)
	if cp := c.For("nvidia", "nvidia/nemotron-3-ultra-550b-a55b"); !cp.Tools || cp.ContextWindow != 1000000 || cp.MaxOutput != 8192 {
		t.Errorf("nemotron 3 ultra: %+v", cp)
	}
	for _, model := range []string{"mistral-large-latest", "mistral-large-4", "mistral-large-4-0"} {
		if cp := c.For("mistral", model); cp.ContextWindow != 1048576 || !cp.Vision {
			t.Errorf("%s: %+v", model, cp)
		}
	}
	for _, model := range []string{"claude-opus-5-5-max", "claude-sonnet-5-5-max"} {
		if cp := c.For("antigravity", model); cp.MaxOutput != 128000 || cp.ContextWindow != 1000000 || cp.ThinkingFormat != "claude-adaptive" {
			t.Errorf("%s: %+v", model, cp)
		}
	}
	if cp := c.For("anthropic", "claude-opus-5-5"); cp.MaxOutput != 64000 {
		t.Errorf("the base Opus 5.5 limit moved: %+v", cp)
	}
}

func TestOnlyHydrogenReadsALongDecodeAndOnlyThereDoesTheCeilingMove(t *testing.T) {
	c := caps(t)
	for _, tc := range []struct {
		provider, model string
		long            bool
	}{
		{"gemini", "gemini-4-hydrogen", true},
		{"antigravity", "gemini-4-hydrogen-high", true},
		{"antigravity", "gemini-4-hydrogen", true},
		{"gemini", "gemini-4-argon", false},
		{"antigravity", "gemini-4-carbon", false},
		{"gemini", "gemini-3.8-flash", false},
		{"openai", "gpt-6", false},
	} {
		got := c.For(tc.provider, tc.model)
		if got.LongDecode != tc.long {
			t.Errorf("%s/%s: longDecode %v", tc.provider, tc.model, got.LongDecode)
		}
		if got.MaxOutput != 65536 && tc.long {
			t.Errorf("%s/%s: one hop returns at most 64K, got %d", tc.provider, tc.model, got.MaxOutput)
		}
	}
	if got := c.For("gemini", "gemini-4-hydrogen"); !got.Reasoning || got.ThinkingFormat != "gemini-level" || got.ThinkingCanOff {
		t.Errorf("the family's own rules were lost behind the longer row: %+v", got)
	}
}

func TestQwenModelsAreAskedToThinkTheQwenWay(t *testing.T) {
	c := caps(t)
	for _, tc := range []struct{ provider, model string }{
		{"qwencloud", "qwen4-max"}, {"qwencloud", "qwen3.8-flash"}, {"qwencloud", "qwen3.7-plus"},
		{"qwencloud", "qwen3.6-flash"}, {"alitp-intl", "qwen3.7-max"}, {"alicode", "qwen3-coder-plus"},
		{"alicode-intl", "qwen3.5-plus"}, {"alims-intl", "qwen3-max"}, {"openrouter", "qwen/qwen3-235b-a22b"},
		{"openai", "qwen2.5-vl-72b"}, {"openai", "qwen3-omni-flash"},
	} {
		got := c.For(tc.provider, tc.model)
		if got.ThinkingFormat != "qwen" || !got.Reasoning {
			t.Errorf("%s/%s: %+v", tc.provider, tc.model, got)
		}
	}
	if got := c.For("openai", "qwq-32b"); got.ThinkingFormat != "qwen" || got.ThinkingCanOff {
		t.Errorf("qwq always thinks: %+v", got)
	}
	if got := c.For("openai", "qwen3-max"); got.Vision || got.ContextWindow != 1000000 || got.MaxOutput != 65536 {
		t.Errorf("Alibaba's Max line is text only: %+v", got)
	}
	if got := c.For("qwencloud", "deepseek-v4-pro-0813"); got.ThinkingFormat != "deepseek" {
		t.Errorf("a row of another family on the same provider changed: %+v", got)
	}
	if got := c.For("qwencloud", "qwen4-max"); got.MaxOutput != 393216 || got.ContextWindow != 1000000 {
		t.Errorf("the provider row's limits were lost: %+v", got)
	}
	if got := c.For("openai", "gpt-4o"); got.ThinkingFormat == "qwen" {
		t.Errorf("another family was given the shape: %+v", got)
	}
	if got := c.For("openai", "qwen3-vl-plus"); !got.Vision || got.ThinkingFormat != "qwen" {
		t.Errorf("a Qwen vision model: %+v", got)
	}
}

func TestTheQwenLevelsAreTheBaseSet(t *testing.T) {
	c := caps(t)
	if got := c.Levels("qwencloud", "qwen4-max", nil); !slices.Equal(got, []string{"none", "low", "medium", "high"}) {
		t.Errorf("qwen4-max: %v", got)
	}
	if got := c.Levels("alitp-intl", "qwen3.7-max", nil); !slices.Equal(got, []string{"none", "low", "medium", "high"}) {
		t.Errorf("qwen3.7-max: %v", got)
	}
}

func TestAProviderMayNameTheQwenShapeForAllItsModels(t *testing.T) {
	ok := `{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example/v1/chat","thinkingFormat":"qwen"},"models":[]}`
	if _, err := Load(fstest.MapFS{"data/a.json": file(ok)}, "data"); err != nil {
		t.Fatalf("the shape the writer reads was refused: %v", err)
	}
}

func TestTheMiMoFamilyReadsImages(t *testing.T) {
	c := caps(t)
	for _, model := range []string{"mimo-v2.6-flash", "mimo-v2.6-pro", "mimo-v2.5-pro", "mimo-v2-omni", "mimo-v2-flash"} {
		if got := c.For("xiaomi-mimo", model); !got.Vision || !got.Reasoning {
			t.Errorf("%s: %+v", model, got)
		}
	}
}

// The families every provider shares a name with answer vision and reasoning the
// same way, and a provider that gates one says so with a row of its own.
func TestTheSharedFamiliesAnswerVisionAndReasoning(t *testing.T) {
	c := caps(t)
	for _, tc := range []struct {
		provider, model string
		vision, reason  bool
	}{
		{"kimi", "kimi-k2.6", true, true},
		{"kimi", "k3.1-pro", true, true},
		{"opencode-go", "kimi-k2.5", true, true},
		{"codebuddy-intl", "kimi-k2.6", true, true},
		{"minimax", "MiniMax-M3", true, true},
		{"minimax", "MiniMax-M2.7", false, true},
		{"openrouter", "minimax/minimax-m3", true, true},
		{"deepseek", "deepseek-v4.1-flash", true, true},
		{"deepseek", "deepseek-chat", false, false},
		{"alitp-intl", "qwen3.7-max", false, true},
		{"alitp-intl", "qwen4-max", true, true},
		{"openai", "qwen3-max", false, true},
		{"openai", "gpt-oss-120b", false, true},
		{"openai", "gpt-4-turbo", true, false},
		{"openai", "claude-3-5-sonnet", true, false},
		{"openai", "grok-code-fast-1", false, true},
		{"openai", "llama-4-maverick", true, false},
		{"openai", "o1-mini", false, true},
		{"nous", "meituan/longcat-2.5-preview:free", true, true},
		{"opencode-go", "claude-haiku-5-5", true, true},
		{"codebuddy-intl", "glm-5v-turbo", true, true},
		{"xiaomi-mimo", "mimo-v2.6-flash", true, true},
		// The one provider that answers a shared name differently.
		{"commandcode", "xiaomi/mimo-v2.5-pro", false, true},
	} {
		got := c.For(tc.provider, tc.model)
		if got.Vision != tc.vision || got.Reasoning != tc.reason {
			t.Errorf("%s/%s: vision=%v reasoning=%v, want %v %v", tc.provider, tc.model, got.Vision, got.Reasoning, tc.vision, tc.reason)
		}
	}
}
