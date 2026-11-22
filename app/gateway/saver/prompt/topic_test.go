package prompt

import (
	"strings"
	"testing"
	"unicode/utf8"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/relevance"
)

func TestAMatchLeavesGreetingsAndArithmeticAlone(t *testing.T) {
	topic := Topic{Name: "x", Description: "terse answers", Keywords: []string{"terse", "brief"}, Triggers: []string{"be concise"}, Threshold: 0.1}
	for _, q := range []string{"hi", "hello", "2 + 2", "2+2", "", "   "} {
		if topic.Match(q) {
			t.Errorf("%q matched", q)
		}
	}
}

func TestAMatchNeedsADistinctiveWord(t *testing.T) {
	topic := Topic{Name: "ponytail", Description: "minimal code and deletion", Keywords: []string{"refactor", "simplify"}, Threshold: 0.3}
	if !topic.Match("please simplify the handler") {
		t.Fatal("a keyword did not match")
	}
	if topic.Match("What is the capital of France?") {
		t.Fatal("an unrelated question matched")
	}
}

func TestAMatchScoresAgainstItsPeers(t *testing.T) {
	mine := Topic{Name: "mine", Description: "terse answers", Keywords: []string{"terse"}, Threshold: 0.35}
	peer := Topic{Name: "peer", Description: "minimal code", Keywords: []string{"refactor"}}
	alone := mine.Match("terse", mine)
	withPeer := mine.Match("terse", mine, peer)
	if !alone || !withPeer {
		t.Fatalf("a keyword missed: alone=%v withPeer=%v", alone, withPeer)
	}
}

func TestLatestUserTakesTheLastNonEmptyUserTurn(t *testing.T) {
	r := &translate.Request{Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart("be exact")},
		{Role: translate.RoleUser, Content: translate.TextPart("first")},
		{Role: translate.RoleAssistant, Content: translate.TextPart("answer")},
		{Role: translate.RoleUser, Content: translate.TextPart("  ")},
		{Role: translate.RoleUser, Content: translate.TextPart(" refactor this ")},
	}}
	if got := LatestUser(r); got != "refactor this" {
		t.Fatalf("%q", got)
	}
	if got := LatestUser(nil); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestLatestUserIsCutOnABoundary(t *testing.T) {
	long := strings.Repeat("é", 5000)
	r := &translate.Request{Messages: []translate.Message{{Role: translate.RoleUser, Content: translate.TextPart(long)}}}
	got := LatestUser(r)
	if len(got) > relevance.QueryBytes || !utf8.ValidString(got) {
		t.Fatalf("cut to %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}
