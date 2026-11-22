package skill

import (
	"math"
	"strings"
	"testing"
)

func corpus() index {
	return buildIndex([]doc{
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
	ix := buildIndex([]doc{{ID: "x", Description: "alpha beta"}}, localWeights)
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
	ix := buildIndex([]doc{{ID: "x", Description: strings.Join(words, " ")}}, localWeights)
	// One of forty normalized terms is about 0.285 once the 1.8 scale is applied.
	if got := ix.Score(words[7], 0.2); len(got) != 1 {
		t.Fatalf("a weak match was dropped: %+v", got)
	}
	if got := ix.Score(words[7], 0.3); len(got) != 0 {
		t.Fatalf("a weak match cleared 0.3: %+v", got)
	}
}

func TestScoreOfAnEmptyIndex(t *testing.T) {
	var ix index
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

// The expected scores below are worked out on paper, not read back from the
// code. A document's terms are weighted by how often its fields repeat them and
// scaled to unit length, a one-word query weighs 1, and the score is the product
// times 1.8, plus 0.35 when a trigger is present.
func TestScoresMatchTheArithmeticWorkedByHand(t *testing.T) {
	near := func(got, want float64) bool { return math.Abs(got-want) < 1e-9 }
	t.Run("four equal terms: each is half the unit vector, 0.5 x 1.8", func(t *testing.T) {
		ix := buildIndex([]doc{{ID: "x", Description: "alpha beta gamma delta"}}, localWeights)
		got := ix.Score("alpha", 0)
		if len(got) != 1 || !near(got[0].Score, 0.9) {
			t.Fatalf("score %+v, want 0.9", got)
		}
	})
	t.Run("a name counts three times: 1/sqrt(12) x 1.8", func(t *testing.T) {
		ix := buildIndex([]doc{{ID: "x", Name: "zeta", Description: "alpha beta gamma"}}, localWeights)
		got := ix.Score("alpha", 0)
		if len(got) != 1 || !near(got[0].Score, 1/math.Sqrt(12)*1.8) {
			t.Fatalf("score %+v, want %v", got, 1/math.Sqrt(12)*1.8)
		}
	})
	t.Run("a trigger present adds exactly 0.35 to a cosine below the cap", func(t *testing.T) {
		// Twenty-four description words and the trigger "zzzz" are twenty-five
		// terms of equal weight: each is a fifth of the unit vector, 0.2 x 1.8 =
		// 0.36. A query holding the trigger gets the boost on top; one that does
		// not, does not.
		var words []string
		for i := range 24 {
			words = append(words, "w"+string(rune('a'+i/10))+string(rune('a'+i%10)))
		}
		ix := buildIndex([]doc{{ID: "x", Description: strings.Join(words, " "), Triggers: []string{"zzzz"}}}, localWeights)
		if got := ix.Score("zzzz", 0); len(got) != 1 || !near(got[0].Score, 0.36+0.35) {
			t.Fatalf("with the trigger %+v, want %v", got, 0.36+0.35)
		}
		if got := ix.Score(words[3], 0); len(got) != 1 || !near(got[0].Score, 0.36) {
			t.Fatalf("without the trigger %+v, want 0.36", got)
		}
	})
	t.Run("a term in fewer documents weighs more, by the idf ratio", func(t *testing.T) {
		// Identical fillers in both documents would share df 2; distinct ones of
		// the same shape give both documents the same vector length.
		ix := buildIndex([]doc{
			{ID: "a", Description: "aaa1 aab1 aac1 aad1 aae1 aaf1 aag1 aah1 aai1 aaj1 common rare"},
			{ID: "b", Description: "bba1 bbb1 bbc1 bbd1 bbe1 bbf1 bbg1 bbh1 bbi1 bbj1 common unique"},
		}, localWeights)
		// df: common 2, every other term 1; docs 2. idf(common) = ln(3/3)+1 = 1 and
		// idf(df 1) = ln(3/2)+1. Each vector is eleven terms at the larger idf
		// and one at 1.
		hi := math.Log(1.5) + 1
		n := math.Sqrt(11*hi*hi + 1)
		byID := func(ms []Match, id string) float64 {
			for _, m := range ms {
				if m.ID == id {
					return m.Score
				}
			}
			return -1
		}
		common := ix.Score("common", 0)
		if !near(byID(common, "a"), 1/n*1.8) || !near(byID(common, "b"), 1/n*1.8) {
			t.Fatalf("common %+v, want %v in both", common, 1/n*1.8)
		}
		if got := byID(ix.Score("rare", 0), "a"); !near(got, hi/n*1.8) {
			t.Fatalf("rare %v, want %v", got, hi/n*1.8)
		}
	})
}
