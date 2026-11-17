package upstream

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

const testPrices = `{
  "free": ["cline-free/"],
  "models": {
    "deepseek-chat": {"input": 0.14, "output": 0.28},
    "gpt-4o": {"input": 2.5, "output": 10, "cached": 1.25}
  },
  "providers": {"github": {"gpt-4o": {"input": 9, "output": 9}}},
  "patterns": [
    {"pattern": "claude-opus-*", "rates": {"input": 5, "output": 25}},
    {"pattern": "claude-*", "rates": {"input": 3, "output": 15}},
    {"pattern": "gpt-4.*", "rates": {"input": 1, "output": 1}}
  ]
}`

func table(t *testing.T) *Table {
	t.Helper()
	tb, err := NewTable([]byte(testPrices), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func TestTheEmbeddedPricesLoad(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	tb, err := DefaultTable(reg)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tb.Lookup("openai", "gpt-4o"); !ok || r.Input != 2.5 {
		t.Fatalf("gpt-4o %+v %v", r, ok)
	}
	if r, ok := tb.Lookup("github", "gpt-5.3-codex"); !ok || r.Input != 1.75 {
		t.Fatalf("github codex %+v %v", r, ok)
	}
}

func TestLookupOrder(t *testing.T) {
	tb := table(t)
	over := Overrides{"openai": {"gpt-4o": {Input: 100, Output: 100}}}

	if r, _ := tb.WithOverrides(over).Lookup("openai", "gpt-4o"); r.Input != 100 {
		t.Fatalf("the override lost: %+v", r)
	}
	if r, _ := tb.Lookup("github", "gpt-4o"); r.Input != 9 {
		t.Fatalf("the provider price lost to the canonical one: %+v", r)
	}
	if r, _ := tb.Lookup("openai", "gpt-4o"); r.Input != 2.5 {
		t.Fatalf("canonical %+v", r)
	}
	if r, ok := tb.Lookup("x", "claude-opus-9"); !ok || r.Input != 5 {
		t.Fatalf("the first pattern should win: %+v %v", r, ok)
	}
	if r, ok := tb.Lookup("x", "claude-haiku-9"); !ok || r.Input != 3 {
		t.Fatalf("the generic pattern: %+v %v", r, ok)
	}
}

func TestAnOverrideBeatsTheProviderPrice(t *testing.T) {
	tb := table(t).WithOverrides(Overrides{"github": {"gpt-4o": {Input: 50, Output: 50}}})
	if r, _ := tb.Lookup("github", "gpt-4o"); r.Input != 50 {
		t.Fatalf("%+v", r)
	}
}

func TestAnEmptyOverrideMakesAModelFree(t *testing.T) {
	raw := map[string]string{"openai": `{"gpt-4o": {}}`}
	o := ParseOverrides(raw, func(string, ...any) {})
	r, ok := table(t).WithOverrides(o).Lookup("openai", "gpt-4o")
	if !ok || r != (Rates{}) {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestTheFreeNamespaceBeatsTheCanonicalPrice(t *testing.T) {
	// the vendor-prefix strip would otherwise turn this into deepseek-chat
	r, ok := table(t).Lookup("cline", "cline-free/deepseek-chat")
	if !ok || r != (Rates{}) {
		t.Fatalf("a free model inherited a paid rate: %+v %v", r, ok)
	}
	if r, _ := table(t).Lookup("cline", "CLINE-FREE/deepseek-chat"); r != (Rates{}) {
		t.Fatalf("the namespace is case-sensitive: %+v", r)
	}
}

func TestAVendorPrefixFallsBackToTheBareModel(t *testing.T) {
	if r, ok := table(t).Lookup("openrouter", "deepseek/deepseek-chat"); !ok || r.Input != 0.14 {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestPatternsAreAnchoredAndCaseInsensitive(t *testing.T) {
	tb := table(t)
	if _, ok := tb.Lookup("x", "my-claude-thing"); ok {
		t.Fatal("a pattern matched in the middle of a name")
	}
	if r, ok := tb.Lookup("x", "CLAUDE-opus-7"); !ok || r.Input != 5 {
		t.Fatalf("%+v %v", r, ok)
	}
	if _, ok := tb.Lookup("x", "gpt-4x5"); ok {
		t.Fatal("a dot in a pattern matched any character")
	}
}

func TestUnknownAndOversizedNamesHaveNoPrice(t *testing.T) {
	tb := table(t)
	if _, ok := tb.Lookup("x", "no-such-model"); ok {
		t.Fatal("an unknown model has a price")
	}
	if _, ok := tb.Lookup("x", ""); ok {
		t.Fatal("an empty name has a price")
	}
	if _, ok := tb.Lookup("x", "claude-"+strings.Repeat("a", 300)); ok {
		t.Fatal("a name over 256 bytes has a price")
	}
	if _, known := tb.Cost("x", "no-such-model", Usage{Prompt: 10}); known {
		t.Fatal("an unknown cost was reported as known")
	}
}

func TestProvidersAreTriedUnderTheirAlias(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	tb, err := NewTable([]byte(`{"providers":{"gh":{"m":{"input":7,"output":7}}}}`), reg)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tb.Lookup("github", "m"); !ok || r.Input != 7 {
		t.Fatalf("a price filed under the alias was not found by id: %+v %v", r, ok)
	}
	if r, ok := tb.Lookup("gh", "m"); !ok || r.Input != 7 {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestCostRules(t *testing.T) {
	r := Rates{Input: 2, Output: 10, Cached: 0.5, Reasoning: 20, CacheCreation: 4}
	cases := []struct {
		name string
		u    Usage
		want float64
	}{
		{"plain", Usage{Prompt: 1_000_000, Completion: 1_000_000}, 12},
		{"cached share charged at its own rate", Usage{Prompt: 1_000_000, Cached: 600_000}, (400_000*2 + 600_000*0.5) / 1e6},
		{"creation share", Usage{Prompt: 1_000_000, CacheCreation: 250_000}, (750_000*2 + 250_000*4) / 1e6},
		{"reasoning share", Usage{Completion: 1_000_000, Reasoning: 400_000}, (600_000*10 + 400_000*20) / 1e6},
		{"cached above the prompt is cut back", Usage{Prompt: 100, Cached: 900}, 100 * 0.5 / 1e6},
		{"reasoning above the completion is cut back", Usage{Completion: 100, Reasoning: 900}, 100 * 20 / 1e6},
		{"negative counts are zero", Usage{Prompt: -5, Completion: -5, Cached: -5}, 0},
		{"the prompt floor is zero", Usage{Prompt: 100, Cached: 80, CacheCreation: 80}, (80*0.5 + 20*4) / 1e6},
	}
	for _, c := range cases {
		if got := r.Cost(c.u); !near(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOmittedRatesFallBackToInputAndOutput(t *testing.T) {
	tb, err := NewTable([]byte(`{"models":{"m":{"input":2,"output":8}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := tb.Lookup("", "m")
	if r.Cached != 2 || r.CacheCreation != 2 || r.Reasoning != 8 {
		t.Fatalf("%+v", r)
	}
	// an explicit zero is a free rate and must not fall back
	tb, _ = NewTable([]byte(`{"models":{"m":{"input":2,"output":8,"cached":0}}}`), nil)
	if r, _ := tb.Lookup("", "m"); r.Cached != 0 {
		t.Fatalf("an explicit zero fell back: %+v", r)
	}
}

func TestMalformedOverridesAreSkippedWithoutLeakingValues(t *testing.T) {
	var lines []string
	log := func(f string, _ ...any) { lines = append(lines, f) }
	o := ParseOverrides(map[string]string{
		"openai": `{"good": {"input": 1, "output": 2}, "bad": {"input": -4}, "typo": {"inptu": 9}, "str": {"input": "secret-123"}}`,
		"broken": `not json`,
	}, log)
	if _, ok := o["openai"]["good"]; !ok {
		t.Fatal("a good entry was dropped")
	}
	for _, m := range []string{"bad", "typo", "str"} {
		if _, ok := o["openai"][m]; ok {
			t.Errorf("malformed entry %q was kept", m)
		}
	}
	if len(o["broken"]) != 0 || len(lines) != 4 {
		t.Fatalf("lines %d: %v", len(lines), lines)
	}
}

func TestTheWarningsNameNoValue(t *testing.T) {
	var out []string
	ParseOverrides(map[string]string{"p": `{"m": {"input": "secret-123"}}`}, func(f string, a ...any) {
		out = append(out, strings.TrimSpace(f))
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	})
	if strings.Contains(strings.Join(out, " "), "secret-123") {
		t.Fatalf("a value leaked: %v", out)
	}
}

func TestTheLoaderRefusesBadPrices(t *testing.T) {
	for name, body := range map[string]string{
		"negative":      `{"models":{"m":{"input":-1}}}`,
		"unknown field": `{"mystery":1}`,
		"not json":      `[`,
	} {
		if _, err := NewTable([]byte(body), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAStoredCardWithTheUnderscoreSpellingPrices(t *testing.T) {
	o := ParseOverrides(map[string]string{
		"claude": `{"m": {"input": 3, "output": 15, "cache_creation": 4.5}}`,
	}, func(string, ...any) {})
	r, ok := o["claude"]["m"]
	if !ok || !near(r.CacheCreation, 4.5) || !near(r.Input, 3) {
		t.Fatalf("%+v %v", r, ok)
	}
}

func TestTheCamelSpellingWinsWhenARowCarriesBoth(t *testing.T) {
	r, err := ParseCard([]byte(`{"input": 1, "cacheCreation": 7, "cache_creation": 9}`))
	if err != nil || !near(r.CacheCreation, 7) {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestACardWeWriteUsesTheStoredSpelling(t *testing.T) {
	in := Rates{Input: 1, Output: 2, Cached: 0.5, Reasoning: 2, CacheCreation: 1.5}
	body, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"cache_creation":1.5`) || strings.Contains(string(body), "cacheCreation") {
		t.Fatalf("%s", body)
	}
	if out, err := ParseCard(body); err != nil || out != in {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestAPokeAtACardRefusesAnUnknownFieldAndKeepsNoValue(t *testing.T) {
	for name, body := range map[string]string{
		"unknown":  `{"inptu": 9}`,
		"negative": `{"input": -4}`,
		"text":     `{"input": "secret-123"}`,
	} {
		_, err := ParseCard([]byte(body))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if err != nil && strings.Contains(err.Error(), "secret-123") {
			t.Errorf("%s: the value leaked: %v", name, err)
		}
	}
}

func TestCardsLaysTheOverridesOverTheEmbeddedRows(t *testing.T) {
	tb := table(t).WithOverrides(Overrides{
		"github": {"gpt-4o": {Input: 1}},
		"own":    {"m": {Input: 2, Output: 3}},
	})
	cards := tb.Cards()
	if cards["github"]["gpt-4o"].Input != 1 {
		t.Fatalf("an override did not win: %+v", cards["github"])
	}
	if cards["own"]["m"].Output != 3 {
		t.Fatalf("a provider only in the overrides is missing: %+v", cards)
	}
	if _, ok := tb.providers["github"]; ok && tb.providers["github"]["gpt-4o"].Input != 9 {
		t.Fatalf("the view rewrote the baseline: %+v", tb.providers["github"])
	}
}
