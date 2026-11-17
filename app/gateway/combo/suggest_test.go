package combo

import (
	"reflect"
	"strings"
	"testing"
)

func TestTiersAreReadFromTheName(t *testing.T) {
	for model, want := range map[string]int{
		"openai/o3":                  TierReasoning,
		"deepseek/deepseek-reason":   TierReasoning,
		"anthropic/claude-opus-5":    TierFlagship,
		"openai/gpt-5.2":             TierFlagship,
		"google/gemini-2.5-flash-8b": TierMini,
		"openai/gpt-4.1-nano":        TierNano,
		"openai/gpt-4o-mini":         TierMini,
		"acme/free-model":            TierFallback,
		"openai/gpt-4o":              TierStandard,
	} {
		if got := TierOf(model); got != want {
			t.Errorf("%s: tier %d, want %d", model, got, want)
		}
	}
}

func TestAShortPromptAsksOnlyForASmallModel(t *testing.T) {
	p := Analyse("hi, what is the capital of France? thanks")
	if !p.Mini || p.Code || p.Reasoning {
		t.Fatalf("%+v", p)
	}
	p = Analyse("refactor this function and debug the api call")
	if !p.Code || p.Mini {
		t.Fatalf("%+v", p)
	}
	p = Analyse("Prove the theorem and derive the trade-off, step by step")
	if !p.Reasoning || p.Mini {
		t.Fatalf("%+v", p)
	}
	long := Analyse(strings.Repeat("x", 600))
	if !long.Reasoning {
		t.Fatalf("a long prompt is not reasoning: %+v", long)
	}
}

func TestTheBoostLiftsTheTierThePromptAsksFor(t *testing.T) {
	reasoning := Profile{Reasoning: true}
	if got := Boost(TierReasoning, reasoning, "openai/o3"); got <= 1.0 {
		t.Fatalf("boost %v", got)
	}
	if got := Boost(TierMini, reasoning, "openai/gpt-4o-mini"); got != 1.0 {
		t.Fatalf("a reasoning prompt boosted a mini model: %v", got)
	}
	coder := Profile{Code: true}
	if got := Boost(TierStandard, coder, "openai/gpt-5-codex"); got <= 1.0 {
		t.Fatalf("a code prompt did not lift a code model: %v", got)
	}
	if got := Boost(TierStandard, coder, "acme/plain"); got != 1.0 {
		t.Fatalf("a plain model was lifted: %v", got)
	}
}

func candidates() []Candidate {
	return []Candidate{
		{Model: "a/o3", Reliability: 0.9, LatencyMs: 3000, CostPerM: 8, KnownCost: true},
		{Model: "b/claude-opus-5", Reliability: 0.9, LatencyMs: 1500, CostPerM: 4, KnownCost: true},
		{Model: "c/gpt-4o", Reliability: 0.9, LatencyMs: 900, CostPerM: 1, KnownCost: true},
		{Model: "d/gpt-4o-mini", Reliability: 0.9, LatencyMs: 200, CostPerM: 0.1, KnownCost: true},
	}
}

func TestTheSuggestionKeepsOneModelOfEachTierFirst(t *testing.T) {
	got := Rank(candidates(), Weights{Reliability: 1}, Profile{}, 4)
	if !reflect.DeepEqual(got, []string{"a/o3", "b/claude-opus-5", "c/gpt-4o", "d/gpt-4o-mini"}) {
		t.Fatalf("%v", got)
	}
}

func TestTheSuggestionFillsTheRemainingSlotsByScore(t *testing.T) {
	list := append(candidates(), Candidate{Model: "e/gpt-4o-mini", Reliability: 0.5, LatencyMs: 100, CostPerM: 0.1, KnownCost: true})
	got := Rank(list, Weights{Latency: 1}, Profile{}, 5)
	// The faster mini model takes the mini slot, and the other one fills the
	// last place by score.
	if len(got) != 5 || got[3] != "e/gpt-4o-mini" || got[4] != "d/gpt-4o-mini" {
		t.Fatalf("fill order: %v", got)
	}
}

func TestTheLimitCapsTheSuggestion(t *testing.T) {
	if got := Rank(candidates(), DefaultWeights, Profile{}, 2); len(got) != 2 {
		t.Fatalf("%v", got)
	}
	if got := Rank(nil, DefaultWeights, Profile{}, 7); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestAModelWithNoPriceNeverWinsOnCost(t *testing.T) {
	cheap := []Candidate{
		{Model: "a/known", Reliability: 0.5, LatencyMs: 100, CostPerM: 9, KnownCost: true},
		{Model: "b/unknown", Reliability: 0.5, LatencyMs: 100, KnownCost: false},
	}
	w := Weights{Cost: 1}
	if got := Rank(cheap, w, Profile{}, 1); got[0] != "a/known" {
		t.Fatalf("an unpriced model won the cost weight: %v", got)
	}
}

func TestAWeightsZeroVectorIsAValidChoice(t *testing.T) {
	// A caller who sends nothing gets the defaults; one who sends zeros gets
	// the quality tier order alone.
	got := Rank(candidates(), Weights{}, Profile{}, 4)
	if len(got) != 4 {
		t.Fatalf("%v", got)
	}
}
