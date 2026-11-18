package combo

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Model tiers, best first. The numbers are the quality score the ranking uses,
// so a tier is one value, not a label with a hidden table.
const (
	TierReasoning = 100
	TierFlagship  = 80
	TierStandard  = 60
	TierMini      = 40
	TierNano      = 20
	TierFallback  = 10
)

// tierRules are tried in order and the first match wins: a model named "mini"
// that reasons is a reasoning model.
var tierRules = []struct {
	tier int
	re   *regexp.Regexp
}{
	{TierReasoning, regexp.MustCompile(`(?:^|[-/])(?:o1|o3|o4|r1)(?:$|[-.])|reason|think|qwq`)},
	{TierFlagship, regexp.MustCompile(`opus|sonnet|gpt-5|gpt-6|grok-4|xlarge|(?:^|[-/])pro(?:$|[-.])`)},
	{TierNano, regexp.MustCompile(`nano`)},
	{TierMini, regexp.MustCompile(`mini|flash|haiku|lite|small`)},
	{TierFallback, regexp.MustCompile(`free|preview`)},
}

// TierOf places a model name in a tier.
func TierOf(model string) int {
	m := strings.ToLower(model)
	for _, rule := range tierRules {
		if rule.re.MatchString(m) {
			return rule.tier
		}
	}
	return TierStandard
}

var (
	reasoningWords = regexp.MustCompile(`\b(prove|derive|why|analy[sz]|reason|step by step|in depth|compare|trade-?offs?)\b`)
	codeWords      = regexp.MustCompile(`\b(code|function|refactor|debug|bug|compile|api|regex|sql|test case)\b`)
	simpleWords    = regexp.MustCompile(`\b(hi|hello|thanks|translate|summari[sz]e|what is|define)\b`)
	// codeModel marks a model family that is known for code.
	codeModel = regexp.MustCompile(`coder|codex|deepseek|claude|gpt-4|gpt-5`)
)

// Profile is what a prompt asks of a model.
type Profile struct {
	Reasoning bool
	Code      bool
	Mini      bool
}

// Analyse reads a prompt. The word lists are this router's own heuristic: they
// only tip the ranking, and a wrong guess costs a place, not an answer.
func Analyse(prompt string) Profile {
	p := strings.ToLower(prompt)
	count := func(re *regexp.Regexp) int { return len(re.FindAllStringIndex(p, -1)) }
	profile := Profile{Reasoning: count(reasoningWords) >= 2 || len(prompt) >= 500}
	profile.Code = count(codeWords) >= 1
	profile.Mini = count(simpleWords) >= 2 && !profile.Reasoning && !profile.Code && len(prompt) < 160
	return profile
}

// Boost is the multiplier a prompt puts on the tier that suits it.
func Boost(tier int, p Profile, model string) float64 {
	switch {
	case p.Reasoning && tier == TierReasoning:
		return 1.3
	case p.Mini && tier == TierMini:
		return 1.2
	case p.Code && codeModel.MatchString(strings.ToLower(model)):
		return 1.15
	}
	return 1
}

// Weights are how much each signal counts. They are not normalised: a caller
// who wants only reliability sends 1,0,0,0.
type Weights struct{ Reliability, Latency, Cost, Quality float64 }

// DefaultWeights is what a caller who names none gets.
var DefaultWeights = Weights{Reliability: 0.4, Latency: 0.3, Cost: 0.2, Quality: 0.1}

// Candidate is one model the ranking may pick.
type Candidate struct {
	Model       string
	Reliability float64
	LatencyMs   float64
	CostPerM    float64
	// KnownCost says the price table had this model. An unknown price scores
	// worst rather than best: a guess must not win on cost.
	KnownCost bool
}

// Rank orders the candidates and answers the model names a combo should carry:
// the best of the tiers a caller picks between first, so a combo is not seven
// copies of one family, then the rest by score. limit caps the answer.
func Rank(in []Candidate, w Weights, p Profile, limit int) []string {
	type scored struct {
		c     Candidate
		tier  int
		value float64
	}
	list := make([]scored, 0, len(in))
	for _, c := range in {
		tier := TierOf(c.Model)
		list = append(list, scored{c: c, tier: tier, value: score(c, tier, w, p)})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].value > list[j].value })

	out := make([]string, 0, min(limit, len(list)))
	used := map[string]bool{}
	take := func(s scored) {
		if len(out) >= limit || used[s.c.Model] {
			return
		}
		used[s.c.Model] = true
		out = append(out, s.c.Model)
	}
	for _, tier := range []int{TierReasoning, TierFlagship, TierStandard, TierMini} {
		for _, s := range list {
			if s.tier == tier {
				take(s)
				break
			}
		}
	}
	for _, s := range list {
		take(s)
	}
	return out
}

// score is one model's weighted value before the prompt's boost.
func score(c Candidate, tier int, w Weights, p Profile) float64 {
	latency := 1 - clamp(c.LatencyMs/10000)
	cost := 0.0
	if c.KnownCost {
		cost = 1 - clamp(c.CostPerM/10)
	}
	v := w.Reliability*c.Reliability + w.Latency*latency + w.Cost*cost + w.Quality*float64(tier)/100
	return v * Boost(tier, p, c.Model)
}

func clamp(v float64) float64 { return math.Min(math.Max(v, 0), 1) }
