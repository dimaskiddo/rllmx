package skill

import (
	"math"
	"strings"
	"testing"
)

func corpus() Index {
	return BuildIndex([]Doc{
		{ID: "simplifier", Name: "Simplifier", Description: "smallest change that works",
			Triggers: []string{"keep it simple"}, Keywords: []string{"refactor", "simplify", "boilerplate"}},
		{ID: "commit-lint", Name: "Commit Lint", Description: "check a commit message against the house rules",
			Triggers: []string{"commit message"}, Keywords: []string{"commit", "lint", "message"}},
	}, localWeights)
}

func TestScoreRanksByOverlap(t *testing.T) {
	ix := corpus()
	got := ix.Score("please refactor this, keep it simple", 0.3)
	if len(got) == 0 || got[0].ID != "simplifier" {
		t.Fatalf("matches %+v", got)
	}
	if got[0].Score > 1 || got[0].Score < 0.3 {
		t.Fatalf("score %v", got[0].Score)
	}
	got = ix.Score("lint this commit message", 0.3)
	if len(got) == 0 || got[0].ID != "commit-lint" {
		t.Fatalf("matches %+v", got)
	}
	if got := ix.Score("what is the capital of France", 0.3); len(got) != 0 {
		t.Fatalf("an unrelated question matched %+v", got)
	}
}

func TestScoreSkipsTrivialQueries(t *testing.T) {
	ix := corpus()
	for _, q := range []string{"", "   ", "hi", "hello", "hey", "2 + 2", "12*3"} {
		if got := ix.Score(q, 0); len(got) != 0 {
			t.Errorf("%q scored %+v", q, got)
		}
	}
}

func TestScoreCapsAtOne(t *testing.T) {
	ix := BuildIndex([]Doc{{ID: "x", Description: "alpha beta"}}, localWeights)
	got := ix.Score("alpha beta", 0)
	if len(got) != 1 || got[0].Score != 1 {
		t.Fatalf("matches %+v", got)
	}
}

func TestScoreRespectsTheFloor(t *testing.T) {
	var words []string
	for i := range 40 {
		words = append(words, "w"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	ix := BuildIndex([]Doc{{ID: "x", Description: strings.Join(words, " ")}}, localWeights)
	// One of forty normalized terms is about 0.285 once the 1.8 scale is applied.
	if got := ix.Score(words[7], 0.2); len(got) != 1 {
		t.Fatalf("a weak match was dropped: %+v", got)
	}
	if got := ix.Score(words[7], 0.3); len(got) != 0 {
		t.Fatalf("a weak match cleared 0.3: %+v", got)
	}
}

func TestTriggerBoostIsFixed(t *testing.T) {
	if got := triggerBoost([]string{"refactor now"}, "please refactor now"); got != 0.35 {
		t.Fatalf("boost %v", got)
	}
	if got := triggerBoost([]string{"abc"}, "abc"); got != 0 {
		t.Fatal("a three-letter trigger scored")
	}
	if got := triggerBoost([]string{"refactor"}, "unrelated"); got != 0 {
		t.Fatal("an absent trigger scored")
	}
}

func TestScoreOfAnEmptyIndex(t *testing.T) {
	var ix Index
	if got := ix.Score("anything at all", 0); got != nil {
		t.Fatalf("matches %+v", got)
	}
}

func TestScoreStaysBoundedOnALargeQuery(t *testing.T) {
	ix := corpus()
	big := strings.Repeat("refactor simplify boilerplate ", 2000)
	for _, m := range ix.Score(big, 0) {
		if m.Score < 0 || m.Score > 1 || math.IsNaN(m.Score) {
			t.Fatalf("score %v", m.Score)
		}
	}
}

func FuzzScore(f *testing.F) {
	for _, seed := range []string{"", "hi", "please refactor", "commit message", "\xff\xfe", "😀 refactor"} {
		f.Add(seed)
	}
	ix := corpus()
	f.Fuzz(func(t *testing.T, query string) {
		for _, floor := range []float64{0, 0.35, 1} {
			seen := map[string]bool{}
			for _, m := range ix.Score(query, floor) {
				if m.Score < 0 || m.Score > 1 || math.IsNaN(m.Score) {
					t.Fatalf("score %v for %q", m.Score, query)
				}
				if seen[m.ID] {
					t.Fatalf("duplicate %q for %q", m.ID, query)
				}
				seen[m.ID] = true
				if m.ID != "simplifier" && m.ID != "commit-lint" {
					t.Fatalf("unknown id %q", m.ID)
				}
			}
		}
	})
}
