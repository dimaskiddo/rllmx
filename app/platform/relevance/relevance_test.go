package relevance

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStemmingFoldsCommonEndings(t *testing.T) {
	for in, want := range map[string]string{
		"refactors": "refactor", "simplify": "simplify", "cleaning": "clean", "removed": "remov",
		"strategies": "strategy", "movement": "move", "creation": "crea", "class": "class",
	} {
		if got := stemWord(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestTokensDropStopWordsAndKeepAStem(t *testing.T) {
	got := Tokens("Please simplify the Handlers, and refactor it")
	for _, want := range []string{"simplify", "handlers", "handler", "refactor"} {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q missing from %v", want, got)
		}
	}
	for _, g := range got {
		if g == "the" || g == "and" || g == "please" {
			t.Errorf("a stop word survived: %q", g)
		}
	}
}

func TestTokensAreBoundedAndNeverSplitACharacter(t *testing.T) {
	long := strings.Repeat("é ", 6000) + strings.Repeat("word ", 5000)
	got := Tokens(long)
	if len(got) > queryTokens+2 {
		t.Fatalf("%d tokens", len(got))
	}
	for _, g := range got {
		if !utf8.ValidString(g) {
			t.Fatalf("token %q is not valid UTF-8", g)
		}
	}
}

func TestTrivialQueriesScoreNothing(t *testing.T) {
	for _, q := range []string{"", "hi", "Hello", "2 + 2", "2+2"} {
		if !Trivial(q) {
			t.Errorf("%q was not trivial", q)
		}
	}
	for _, q := range []string{"hi there", "2 + two", "simplify"} {
		if Trivial(q) {
			t.Errorf("%q was trivial", q)
		}
	}
}

func TestWeighIsUnitLengthAndRareTermsCountMore(t *testing.T) {
	w := Weigh([]string{"a1", "a1", "b2"}, nil, 0)
	var sum float64
	for _, v := range w {
		sum += v * v
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("length %v", sum)
	}
	df := map[string]int{"common": 3, "rare": 1}
	d := Weigh([]string{"common", "rare"}, df, 3)
	if d["rare"] <= d["common"] {
		t.Fatalf("a term every document shares weighed %v against %v", d["common"], d["rare"])
	}
	if len(Weigh(nil, nil, 0)) != 0 {
		t.Fatal("no tokens, no weights")
	}
}

func TestTheTriggerBoostIsFixedAndIgnoresShortPhrases(t *testing.T) {
	if got := TriggerBoost([]string{"keep it simple"}, "please keep it simple"); got != Boost {
		t.Fatalf("boost %v", got)
	}
	if got := TriggerBoost([]string{"Keep It Simple"}, "please keep it simple"); got != Boost {
		t.Fatalf("a trigger written in capitals missed: %v", got)
	}
	if got := TriggerBoost([]string{"fix"}, "please fix it"); got != 0 {
		t.Fatalf("a three-letter trigger boosted: %v", got)
	}
	if got := TriggerBoost(nil, "x"); got != 0 {
		t.Fatalf("no triggers boosted: %v", got)
	}
}

func TestCosineIsCappedAtOne(t *testing.T) {
	v := map[string]float64{"x": 1}
	if got := Cosine(v, v, []string{"keep it simple"}, "keep it simple"); got != 1 {
		t.Fatalf("score %v", got)
	}
	if got := Cosine(v, map[string]float64{"y": 1}, nil, "q"); got != 0 {
		t.Fatalf("disjoint vectors scored %v", got)
	}
}

func TestTokensKeepOnlyWordsOfTwoOrMoreCharactersAndDropEveryStopWord(t *testing.T) {
	for _, w := range []string{"x", "z"} {
		if got := Tokens(w); len(got) != 0 {
			t.Errorf("a one-character word became %v", got)
		}
	}
	if got := Tokens("ab"); len(got) != 1 || got[0] != "ab" {
		t.Errorf("a two-character word gave %v", got)
	}
	// A fixed list, not the table itself: a word dropped from the table must fail.
	for _, w := range []string{"a", "the", "and", "with", "this", "please", "help", "make", "need", "want", "code", "file"} {
		if got := Tokens(w); len(got) != 0 {
			t.Errorf("the stop word %q became %v", w, got)
		}
	}
}
