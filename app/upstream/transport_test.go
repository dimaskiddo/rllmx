package upstream

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

func withTransport(extra string) string {
	return `{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example/v1/chat"` + extra + `},"models":[]}`
}

func TestTheLoaderChecksTheTransport(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"a good transport":   {withTransport(`,"headers":{"X-Title":"t"},"urlSuffix":"?beta=true","retry":{"429":{"attempts":2,"delayMs":500}},"quirks":{"requireClaudeToolType":true},"reasoningInject":"all"`), ""},
		"credential header":  {withTransport(`,"headers":{"Authorization":"x"}`), "credential or framing"},
		"cookie header":      {withTransport(`,"headers":{"Cookie":"x"}`), "credential or framing"},
		"bad header name":    {withTransport(`,"headers":{"a b":"x"}`), "invalid name"},
		"control in value":   {withTransport(`,"headers":{"X-A":"a\nb"}`), "invalid value"},
		"suffix not query":   {withTransport(`,"urlSuffix":"/x"`), "query string"},
		"suffix with space":  {withTransport(`,"urlSuffix":"?a b"`), "query string"},
		"retry on a 200":     {withTransport(`,"retry":{"200":{"attempts":1}}`), "retry is out of range"},
		"retry too many":     {withTransport(`,"retry":{"429":{"attempts":99}}`), "retry is out of range"},
		"retry too long":     {withTransport(`,"retry":{"429":{"attempts":1,"delayMs":999999}}`), "retry is out of range"},
		"unknown quirk":      {withTransport(`,"quirks":{"explode":true}`), "unknown field"},
		"unknown scope":      {withTransport(`,"reasoningInject":"some"`), "reasoningInject"},
		"unknown holder":     {strings.Replace(withTransport(""), "/v1/chat", "/{region}/chat", 1), "placeholder"},
		"credentials in url": {strings.Replace(withTransport(""), "https://x", "https://u:p@x", 1), "plain http"},
		"account id holder":  {strings.Replace(withTransport(""), "/v1/chat", "/accounts/{accountId}/chat", 1), ""},
		"header over auth":   {`{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example/v1","headers":{"X-Api-Key":"k"}},"auth":{"scheme":"raw","header":"x-api-key"},"models":[]}`, "credential or framing"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(fstest.MapFS{"data/a.json": file(c.body)}, "data")
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("a valid file was refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("err %v, want %q", err, c.want)
			}
		})
	}
}

func TestOnlyAnExecutorThatBuildsItsOwnAddressMayLeaveItOut(t *testing.T) {
	own := `{"id":"a","category":"apikey","executor":"azure","transport":{},"models":[]}`
	if _, err := Load(fstest.MapFS{"data/a.json": file(own)}, "data"); err != nil {
		t.Fatalf("azure without an address was refused: %v", err)
	}
	plain := `{"id":"a","category":"apikey","transport":{},"models":[]}`
	if _, err := Load(fstest.MapFS{"data/a.json": file(plain)}, "data"); err == nil {
		t.Fatal("the default executor loaded without an address")
	}
}

func TestAModelsWireIDIsBoundedAndTheRightOneIsSent(t *testing.T) {
	bad := `{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example"},"models":[{"id":"m","upstreamId":"a\nb"}]}`
	if _, err := Load(fstest.MapFS{"data/a.json": file(bad)}, "data"); err == nil || !strings.Contains(err.Error(), "upstream id") {
		t.Fatalf("err %v", err)
	}
	ok := `{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example"},"models":[{"id":"m","upstreamId":"vendor/m"},{"id":"n"}]}`
	reg, err := Load(fstest.MapFS{"data/a.json": file(ok)}, "data")
	if err != nil {
		t.Fatal(err)
	}
	if reg.WireModel("a", "m") != "vendor/m" || reg.WireModel("a", "n") != "n" || reg.WireModel("a", "unlisted") != "unlisted" || reg.WireModel("nope", "m") != "m" {
		t.Fatal("WireModel returned the wrong id")
	}
}

func TestModelReasoningScope(t *testing.T) {
	for in, want := range map[string]string{
		"kimi-k2.5": ReasoningToolCalls, "Kimi-K2.6": ReasoningToolCalls, "moonshotai/kimi-k2": ReasoningToolCalls,
		"deepseek-v4-pro": ReasoningAll, "deepseek-ai/DeepSeek-R1": ReasoningAll, "gpt-5": "", "": "",
	} {
		if got := ModelReasoningScope(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

var parityRow = regexp.MustCompile(`(?m)^\| (?:default|specialized) \| (done|partial)[^|]*\| ([^|]+) \|$`)

// The parity tables of PRD 8 are the list of what must exist: a row marked
// done or partial needs its data file, and nothing may be listed twice.
func TestEveryProviderTheParityTablesCallDoneHasAFile(t *testing.T) {
	raw, err := os.ReadFile("../../docs/prds/08-Executors.md")
	if err != nil {
		t.Skip("the PRD is not beside the code")
	}
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range parityRow.FindAllStringSubmatch(string(raw), -1) {
		for _, id := range strings.Split(m[2], ",") {
			id = strings.TrimSpace(id)
			if seen[id] {
				t.Errorf("%s is listed twice", id)
			}
			seen[id] = true
			if _, ok := reg.ByID(id); !ok {
				t.Errorf("%s is marked %s and has no data file", id, m[1])
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("no row of the parity tables was read: the test has gone blind")
	}
}

func TestTheShippedParamRulesReachTheirProviders(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	has := func(provider, model string, pick func(ParamRule) bool) bool {
		for _, r := range c.ParamRules(provider, model) {
			if pick(r) {
				return true
			}
		}
		return false
	}
	if !has("mistral", "mistral-large-latest", func(r ParamRule) bool { return len(r.Drop) == 1 && r.Drop[0] == "client_metadata" }) {
		t.Error("mistral keeps client_metadata")
	}
	if !has("cerebras", "qwen-3-32b", func(r ParamRule) bool { return len(r.DropMessageFields) == 3 }) {
		t.Error("cerebras keeps replayed reasoning")
	}
	if !has("cloudflare-ai", "@cf/qwen/qwq-32b", func(r ParamRule) bool { return r.FlattenContent }) {
		t.Error("cloudflare-ai takes content parts")
	}
	if !has("volcengine-ark", "Kimi-K2.6", func(r ParamRule) bool { return r.CapOutput == 32768 }) {
		t.Error("volcengine-ark kimi is uncapped")
	}
	if has("volcengine-ark", "GLM-5.1", func(r ParamRule) bool { return r.CapOutput > 0 }) {
		t.Error("the kimi cap reached another model")
	}
	for _, p := range []string{"minimax", "minimax-cn"} {
		if !c.For(p, "MiniMax-M3").DropOutputConfig {
			t.Errorf("%s accepts output_config", p)
		}
	}
	if c.For("anthropic", "claude-opus-5").DropOutputConfig {
		t.Error("anthropic lost output_config")
	}
}

func TestLiteAndLevelsAreCheckedOnAModel(t *testing.T) {
	good := `{"id":"a","category":"apikey","transport":{"baseUrl":"https://x.example"},"models":[{"id":"m","lite":true,"levels":["low","medium","max"]}]}`
	reg, err := Load(fstest.MapFS{"data/a.json": file(good)}, "data")
	if err != nil {
		t.Fatal(err)
	}
	m, ok := reg.Model("a", "m")
	if !ok || !m.Lite || len(m.Levels) != 3 {
		t.Fatalf("%+v %v", m, ok)
	}
	if _, ok := reg.Model("a", "ghost"); ok {
		t.Fatal("an unlisted model has a row")
	}
	for name, levels := range map[string]string{
		"unknown":  `["low","turbo"]`,
		"too many": `["none","minimal","low","medium","high","xhigh","max","ultra","low"]`,
	} {
		bad := strings.Replace(good, `["low","medium","max"]`, levels, 1)
		if _, err := Load(fstest.MapFS{"data/a.json": file(bad)}, "data"); err == nil || !strings.Contains(err.Error(), "level") {
			t.Errorf("%s: err %v", name, err)
		}
	}
}

func TestTheShippedCodexAndGrokFilesDeclareWhatTheirExecutorsNeed(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	cx, _ := reg.ByID("codex")
	if cx.Executor != ExecutorCodex || !cx.ForceStream || !cx.Transport.Quirks.InlineImages {
		t.Errorf("codex: %s force=%v inline=%v", cx.Executor, cx.ForceStream, cx.Transport.Quirks.InlineImages)
	}
	if m, _ := reg.Model("codex", "gpt-6-sol"); !m.Lite || len(m.Levels) == 0 {
		t.Errorf("gpt-6-sol: %+v", m)
	}
	if m, _ := reg.Model("codex", "gpt-5.6-sol"); m.Lite {
		t.Error("gpt-5.6-sol is not a Lite model")
	}
	if reg.WireModel("codex", "gpt-5.6-sol-review") != "gpt-5.6-sol" {
		t.Error("the review model is not sent as its base")
	}
	gk, _ := reg.ByID("grok-cli")
	if gk.Executor != ExecutorGrokCLI || !gk.ForceStream || gk.Transport.Headers["x-grok-client-identifier"] == "" {
		t.Errorf("grok-cli: %+v", gk.Transport.Headers)
	}
	if m, _ := reg.Model("grok-cli", "grok-4.5"); len(m.Levels) == 0 {
		t.Error("grok-4.5 declares no levels")
	}
	if m, _ := reg.Model("grok-cli", "grok-build"); len(m.Levels) != 0 {
		t.Error("grok-build takes no effort")
	}
}

func TestA429IsRetriedInPlaceOnceAfterAWaitAndOnlyWhereTheDataSaysSo(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range reg.All() {
		r, has := e.Transport.Retry[429]
		switch e.ID {
		case "tokenharbor", "vercel-ai-gateway", "grok-cli":
			if !has || r.Attempts != 1 || r.DelayMs < 1000 {
				t.Errorf("%s: %+v, want one retry after a wait", e.ID, r)
			}
		default:
			if has && r.Attempts > 0 {
				t.Errorf("%s retries a 429 in place", e.ID)
			}
		}
	}
}

func TestNoneIsNotARequestableLevelButEveryOtherNameIs(t *testing.T) {
	if IsEffortLevel("none") {
		t.Error("none is switching thinking off, not a level")
	}
	for _, l := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"} {
		if !IsEffortLevel(l) {
			t.Errorf("%s is refused", l)
		}
	}
	for _, l := range []string{"", "banana", "HIGH", "auto"} {
		if IsEffortLevel(l) {
			t.Errorf("%q is accepted", l)
		}
	}
}
