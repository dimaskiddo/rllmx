package translate

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/upstream"
)

func writeWith(t *testing.T, th Thinking, caps upstream.Capability) []byte {
	t.Helper()
	c := NewCtx(FormatOpenAI, FormatOpenAI)
	c.Caps = caps
	out, err := OpenAICodec{}.WriteRequest(&Request{Model: "m", Thinking: th}, c)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNousReadsAReasoningObjectAndNeverReasoningEffort(t *testing.T) {
	caps := upstream.Capability{Reasoning: true, ThinkingCanOff: true, ThinkingFormat: "nous"}
	for name, tc := range map[string]struct {
		th   Thinking
		want string
	}{
		"low":           {Thinking{Mode: "level", Level: "low"}, `{"effort":"low","enabled":true}`},
		"high":          {Thinking{Mode: "level", Level: "high"}, `{"effort":"high","enabled":true}`},
		"xhigh":         {Thinking{Mode: "level", Level: "xhigh"}, `{"effort":"xhigh","enabled":true}`},
		"auto":          {Thinking{Mode: "auto"}, `{"effort":"medium","enabled":true}`},
		"none":          {Thinking{Mode: "none"}, ``},
		"budget":        {Thinking{Mode: "budget", Budget: 1000}, `{"effort":"low","enabled":true}`},
		"a huge budget": {Thinking{Mode: "budget", Budget: 200000}, `{"effort":"max","enabled":true}`},
	} {
		t.Run(name, func(t *testing.T) {
			out := writeWith(t, tc.th, caps)
			if got := gjson.GetBytes(out, "reasoning").Raw; got != tc.want {
				t.Fatalf("reasoning %q, want %q in %s", got, tc.want, out)
			}
			if gjson.GetBytes(out, "reasoning_effort").Exists() {
				t.Fatalf("reasoning_effort is set: %s", out)
			}
		})
	}
}

// The thresholds are the reference's: the same budget gives the same level.
func TestABudgetStandsForTheReferencesLevels(t *testing.T) {
	for budget, want := range map[int]string{
		-1: "", 0: "", 1: "minimal", 768: "minimal", 769: "low", 4096: "low", 4097: "medium",
		16384: "medium", 16385: "high", 28672: "high", 28673: "xhigh", 80384: "xhigh", 80385: "max", 1 << 20: "max",
	} {
		if got := budgetLevel(budget); got != want {
			t.Errorf("%d: %q, want %q", budget, got, want)
		}
	}
}

func TestANousThinkingWithNoLevelIsJustEnabled(t *testing.T) {
	caps := upstream.Capability{Reasoning: true, ThinkingCanOff: true, ThinkingFormat: "nous"}
	out := writeWith(t, Thinking{Mode: "level"}, caps)
	if got := gjson.GetBytes(out, "reasoning").Raw; got != `{"enabled":true}` {
		t.Fatalf("%s", out)
	}
}

func TestKimiLevelsAreTheOnesKimiReads(t *testing.T) {
	can := upstream.Capability{Reasoning: true, ThinkingCanOff: true, ThinkingFormat: "kimi"}
	cannot := upstream.Capability{Reasoning: true, ThinkingCanOff: false, ThinkingFormat: "kimi"}
	for name, tc := range map[string]struct {
		caps upstream.Capability
		th   Thinking
		want string
	}{
		"low":                                    {can, Thinking{Mode: "level", Level: "low"}, "low"},
		"max":                                    {can, Thinking{Mode: "level", Level: "max"}, "max"},
		"xhigh becomes max":                      {can, Thinking{Mode: "level", Level: "xhigh"}, "max"},
		"ultra becomes max":                      {can, Thinking{Mode: "level", Level: "ultra"}, "max"},
		"minimal becomes low":                    {can, Thinking{Mode: "level", Level: "minimal"}, "low"},
		"auto is high":                           {can, Thinking{Mode: "auto"}, "high"},
		"a level Kimi does not read is left out": {can, Thinking{Mode: "level", Level: "none"}, ""},
		"none on a model that can":               {can, Thinking{Mode: "none"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := writeWith(t, tc.th, tc.caps)
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tc.want {
				t.Fatalf("%q, want %q in %s", got, tc.want, out)
			}
			if gjson.GetBytes(out, "reasoning").Exists() {
				t.Fatalf("a reasoning object was written: %s", out)
			}
		})
	}
	// None on a model that cannot switch off is the least effort instead, which
	// is the reference's rule; on a model that can, it is disabled thinking.
	out := writeWith(t, Thinking{Mode: "none"}, cannot)
	if gjson.GetBytes(out, "reasoning_effort").String() != "low" || gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("a model that cannot switch off: %s", out)
	}
	out = writeWith(t, Thinking{Mode: "none"}, can)
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "disabled" || gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("a model that can: %s", out)
	}
	if got := gjson.GetBytes(writeWith(t, Thinking{Mode: "budget", Budget: 1000}, can), "reasoning_effort").String(); got != "low" {
		t.Fatalf("budget 1000: %q", got)
	}
}

func TestTokenRouterSendsItsOwnLevelsAndNothingForOff(t *testing.T) {
	caps := upstream.Capability{Reasoning: true, ThinkingCanOff: true, ThinkingFormat: "tokenrouter"}
	for name, tc := range map[string]struct {
		th   Thinking
		want string
	}{
		"low":      {Thinking{Mode: "level", Level: "low"}, "low"},
		"xhigh":    {Thinking{Mode: "level", Level: "xhigh"}, "xhigh"},
		"max":      {Thinking{Mode: "level", Level: "max"}, "max"},
		"auto":     {Thinking{Mode: "auto"}, ""},
		"none":     {Thinking{Mode: "none"}, ""},
		"a budget": {Thinking{Mode: "budget", Budget: 4096}, "low"},
		"no level": {Thinking{Mode: "level"}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			out := writeWith(t, tc.th, caps)
			if got := gjson.GetBytes(out, "reasoning_effort").String(); got != tc.want {
				t.Fatalf("%q, want %q in %s", got, tc.want, out)
			}
		})
	}
}

func TestOtherOpenAIUpstreamsKeepReasoningEffort(t *testing.T) {
	plain := upstream.Capability{Reasoning: true, ThinkingCanOff: true}
	for th, want := range map[Thinking]string{
		{Mode: "level", Level: "xhigh"}: "xhigh", {Mode: "none"}: "none", {Mode: "level", Level: "minimal"}: "minimal",
	} {
		out := writeWith(t, th, plain)
		if got := gjson.GetBytes(out, "reasoning_effort").String(); got != want {
			t.Errorf("%+v: %q, want %q", th, got, want)
		}
	}
	if out := writeWith(t, Thinking{}, plain); strings.Contains(string(out), "reasoning") {
		t.Errorf("nothing asked: %s", out)
	}
}

func TestAWriterWithNoContextStillWritesReasoningEffort(t *testing.T) {
	out, err := OpenAICodec{}.WriteRequest(&Request{Model: "m", Thinking: Thinking{Mode: "level", Level: "high"}}, nil)
	if err != nil || gjson.GetBytes(out, "reasoning_effort").String() != "high" {
		t.Fatalf("%s %v", out, err)
	}
}

func TestTheKimiAndNousLevelsAreInTheCapabilityData(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := caps.Levels("kimi", "kimi-k3", nil); strings.Join(got, ",") != "low,medium,high,max" {
		t.Errorf("kimi-k3 cannot switch off: %v", got)
	}
	if got := caps.Levels("kimi", "kimi-k2.5", nil); strings.Join(got, ",") != "none,low,medium,high,max" {
		t.Errorf("kimi-k2.5 can: %v", got)
	}
	if got := caps.Levels("nous", "stealth/missingno", nil); strings.Join(got, ",") != "none,low,medium,high" {
		t.Errorf("nous: %v", got)
	}
	if c := caps.For("nous", "stepfun/step-5-preview:free"); c.ThinkingFormat != "nous" || !c.Reasoning {
		t.Errorf("nous row: %+v", c)
	}
}

func TestZaiTurnsThinkingOnWithItsOwnObjectAndOffWithItsOwnSwitch(t *testing.T) {
	can := upstream.Capability{Reasoning: true, ThinkingCanOff: true, ThinkingFormat: "zai"}
	cannot := upstream.Capability{Reasoning: true, ThinkingFormat: "zai"}
	effort := upstream.Capability{Reasoning: true, ThinkingCanOff: true, ThinkingFormat: "zai", ThinkingEffortSupported: true}
	for name, tc := range map[string]struct {
		th   Thinking
		caps upstream.Capability
		want string
	}{
		"on, no effort read":   {Thinking{Mode: "level", Level: "high"}, can, `{"thinking":{"type":"enabled"}}`},
		"off":                  {Thinking{Mode: "none"}, can, `{"enable_thinking":false}`},
		"off on a fixed model": {Thinking{Mode: "none"}, cannot, `{"thinking":{"type":"enabled"}}`},
		"low":                  {Thinking{Mode: "level", Level: "low"}, effort, `{"reasoning_effort":"low","thinking":{"type":"enabled"}}`},
		"minimal":              {Thinking{Mode: "level", Level: "minimal"}, effort, `{"reasoning_effort":"low","thinking":{"type":"enabled"}}`},
		"medium":               {Thinking{Mode: "level", Level: "medium"}, effort, `{"reasoning_effort":"high","thinking":{"type":"enabled"}}`},
		"high":                 {Thinking{Mode: "level", Level: "high"}, effort, `{"reasoning_effort":"high","thinking":{"type":"enabled"}}`},
		"xhigh":                {Thinking{Mode: "level", Level: "xhigh"}, effort, `{"reasoning_effort":"max","thinking":{"type":"enabled"}}`},
		"max":                  {Thinking{Mode: "level", Level: "max"}, effort, `{"reasoning_effort":"max","thinking":{"type":"enabled"}}`},
		"a budget of 100000":   {Thinking{Mode: "budget", Budget: 100000}, effort, `{"reasoning_effort":"max","thinking":{"type":"enabled"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			out := writeWith(t, tc.th, tc.caps)
			got := `{}`
			for _, k := range []string{"enable_thinking", "reasoning_effort", "thinking"} {
				if v := gjson.GetBytes(out, k); v.Exists() {
					got, _ = sjson.SetRaw(got, k, v.Raw)
				}
			}
			var a, b any
			_ = json.Unmarshal([]byte(got), &a)
			_ = json.Unmarshal([]byte(tc.want), &b)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("%s, want %s", got, tc.want)
			}
		})
	}
}
