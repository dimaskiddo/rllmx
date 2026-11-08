package upstream

import (
	"slices"
	"strings"
	"testing"
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

const thinkingJSON = `{"base":["low"],"formats":{},"rules":[],"budgets":{},"claudeSignature":"a","geminiSignature":"b"}`
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
		"thinking rule no levels": {map[string]string{"think": `{"base":["a"],"rules":[{"pattern":"a"}],"claudeSignature":"a","geminiSignature":"b"}`}, "levels"},
		"budget out of range":     {map[string]string{"think": `{"base":["a"],"budgets":{"x":-5},"claudeSignature":"a","geminiSignature":"b"}`}, "range"},
		"no placeholder":          {map[string]string{"think": `{"base":["a"],"claudeSignature":"","geminiSignature":"b"}`}, "signatures"},
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
