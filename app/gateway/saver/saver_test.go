package saver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"rllmx/app/gateway/saver/prompt"
	"rllmx/app/llm/translate"
)

// toolDiff is a tool result the detector recognises and the filter shrinks.
func toolDiff() string {
	var b strings.Builder
	b.WriteString("diff --git a/foo.go b/foo.go\n@@ -1 +1,2 @@\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "-old line %d\n+new line %d\n", i, i)
	}
	return b.String()
}

func requestWith(msgs ...translate.Message) *translate.Request {
	return &translate.Request{Model: "m", Messages: msgs}
}

func toolMessage(text string, failed bool) translate.Message {
	return translate.Message{Role: translate.RoleTool, ToolCallID: "call_1", Content: translate.TextPart(text), Error: failed}
}

func TestApplyCompactsSuccessfulToolResultsOnly(t *testing.T) {
	diff := toolDiff()
	kept := strings.Repeat("+kept line\n", 100)
	r := requestWith(
		translate.Message{Role: translate.RoleSystem, Content: translate.TextPart("be exact")},
		toolMessage(diff, false),
		toolMessage(kept, true),
	)
	st := Apply(context.Background(), nil, r, Options{RTK: true})
	if !st.Changed() || st.Saved <= 0 || len(st.Filters) != 1 || st.Filters[0] != "git-diff" {
		t.Fatalf("stats %+v", st)
	}
	if got := r.Messages[1].Content[0].Text; len(got) >= len(diff) {
		t.Fatalf("tool result not compacted: %d bytes", len(got))
	}
	if got := r.Messages[2].Content[0].Text; got != kept {
		t.Fatal("a failed tool result was rewritten")
	}
}

func TestApplyIsOffWhenNothingIsConfigured(t *testing.T) {
	diff := toolDiff()
	r := requestWith(toolMessage(diff, false))
	if st := Apply(context.Background(), nil, r, Options{}); st.Changed() {
		t.Fatalf("stats %+v", st)
	}
	if r.Messages[0].Content[0].Text != diff {
		t.Fatal("a disabled saver changed the request")
	}
	if st := Apply(context.Background(), nil, nil, Options{RTK: true}); st.Changed() {
		t.Fatal("a nil request was changed")
	}
}

func TestAnAlwaysPromptIsAddedOnce(t *testing.T) {
	r := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("hello")})
	o := Options{Caveman: Prompt{Enabled: true, Level: "full", Mode: "always"}}
	st := Apply(context.Background(), nil, r, o)
	if len(st.Prompts) != 1 || st.Prompts[0] != "caveman" {
		t.Fatalf("stats %+v", st)
	}
	if r.Messages[0].Role != translate.RoleSystem || !strings.Contains(r.Messages[0].Text(), "Respond like terse caveman") {
		t.Fatalf("system message %q", r.Messages[0].Text())
	}
	before := r.Messages[0].Text()
	if st := Apply(context.Background(), nil, r, o); len(st.Prompts) != 0 {
		t.Fatalf("a second run injected again: %+v", st)
	}
	if r.Messages[0].Text() != before {
		t.Fatal("the second run changed the prompt")
	}
}

func TestASystemPromptIsAppendedToAnExistingOne(t *testing.T) {
	r := requestWith(
		translate.Message{Role: translate.RoleSystem, Content: translate.TextPart("Be precise.")},
		translate.Message{Role: translate.RoleUser, Content: translate.TextPart("hey")},
	)
	cfg := Options{Custom: Custom{Enabled: true, Text: "Answer in French."}}
	Apply(context.Background(), nil, r, cfg)
	Apply(context.Background(), nil, r, cfg)
	if len(r.Messages) != 2 || !strings.HasPrefix(r.Messages[0].Text(), "Be precise.") ||
		!strings.Contains(r.Messages[0].Text(), "Answer in French.") {
		t.Fatalf("messages %+v", r.Messages)
	}
	if n := strings.Count(r.Messages[0].Text(), "Answer in French."); n != 1 {
		t.Fatalf("the prompt is in the system text %d times", n)
	}
}

func TestASmartPromptWaitsForItsTopic(t *testing.T) {
	cfg := Options{Ponytail: Prompt{Enabled: true, Level: "full", Mode: "smart"}}
	plain := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("What is the capital of France?")})
	if st := Apply(context.Background(), nil, plain, cfg); len(st.Prompts) != 0 {
		t.Fatalf("injected on an unrelated question: %+v", st)
	}
	onTopic := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("please refactor this, keep it simple")})
	if st := Apply(context.Background(), nil, onTopic, cfg); len(st.Prompts) != 1 {
		t.Fatalf("no injection on its topic: %+v", st)
	}
	invalid := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("simplify")})
	if st := Apply(context.Background(), nil, invalid, Options{Ponytail: Prompt{Enabled: true, Level: "nope", Mode: "always"}}); len(st.Prompts) != 0 {
		t.Fatal("an unknown level injected text")
	}
}

func TestEachTopicScoresItsOwnRequests(t *testing.T) {
	for name, c := range map[string]struct {
		topic prompt.Topic
		query string
		want  bool
	}{
		"ponytail on a refactor request": {ponytailTopic, "please refactor this, keep it simple", true},
		"caveman on a brevity request":   {cavemanTopic, "answer briefly", true},
		"ponytail on an unrelated ask":   {ponytailTopic, "What is the capital of France?", false},
		"caveman on an unrelated ask":    {cavemanTopic, "What is the capital of France?", false},
		"greeting":                       {cavemanTopic, "hi", false},
		"sum":                            {cavemanTopic, "2 + 2", false},
	} {
		if got := c.topic.Match(c.query, saverTopics...); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}

// smartVerdicts is what the smart mode decides for fixed requests, with the real
// caveman and ponytail routing tables. It pins the scorer's output so a change to
// how the topics are scored cannot move a verdict unnoticed.
func smartVerdicts() map[string][2]bool {
	out := map[string][2]bool{}
	for _, q := range []string{
		"please simplify the handler", "be concise about it", "keep it simple and refactor", "What is the capital of France?",
		"terse brief concise", "refactor", "refactoring simplified minimal strategies", "Keep It Simple",
		"explain the history of the roman empire in detail", "hi", "2+2", "drop the filler and answer briefly",
		"this code is over-engineered, remove dead code", "too much boilerplate in this module", "short answer please",
		"compression of tokens", "delete unused helpers", "give me a caveman style answer", "write a poem about the sea",
		"lean native stdlib solution", "yagni", "reduce complexity of the parser", "no filler", "clean up the tests",
	} {
		out[q] = [2]bool{cavemanTopic.Match(q, saverTopics...), ponytailTopic.Match(q, saverTopics...)}
	}
	return out
}

func TestTheSmartTopicsDecideAsTheyAlwaysDid(t *testing.T) {
	want := map[string][2]bool{
		"2+2": {false, false}, "Keep It Simple": {false, true}, "What is the capital of France?": {false, false},
		"be concise about it": {true, false}, "clean up the tests": {false, true}, "compression of tokens": {true, false},
		"delete unused helpers": {false, true}, "drop the filler and answer briefly": {true, false},
		"explain the history of the roman empire in detail": {false, false}, "give me a caveman style answer": {true, false},
		"hi": {false, false}, "keep it simple and refactor": {false, true}, "lean native stdlib solution": {false, true},
		"no filler": {true, false}, "please simplify the handler": {false, true}, "reduce complexity of the parser": {false, true},
		"refactor": {false, true}, "refactoring simplified minimal strategies": {false, true}, "short answer please": {true, false},
		"terse brief concise": {true, false}, "this code is over-engineered, remove dead code": {false, true},
		"too much boilerplate in this module": {false, true}, "write a poem about the sea": {false, false}, "yagni": {false, true},
	}
	got := smartVerdicts()
	if len(got) != len(want) {
		t.Fatalf("%d queries, %d pinned", len(got), len(want))
	}
	for q, w := range want {
		if got[q] != w {
			t.Errorf("%q decided %v, want %v", q, got[q], w)
		}
	}
}

func TestASmartSaverThatDoesNotMatchSaysItSkipped(t *testing.T) {
	cfg := Options{
		Caveman:  Prompt{Enabled: true, Level: "full", Mode: "smart"},
		Ponytail: Prompt{Enabled: true, Level: "full", Mode: "smart"},
	}
	r := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("What is the capital of France?")})
	st := Apply(context.Background(), nil, r, cfg)
	if strings.Join(st.Skipped, ",") != "caveman,ponytail" {
		t.Fatalf("skipped %v", st.Skipped)
	}
	if st.Changed() || len(st.Prompts) != 0 {
		t.Fatalf("a skipped saver acted: %+v", st)
	}
}

func TestADisabledSaverSaysNothing(t *testing.T) {
	r := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("hi")})
	st := Apply(context.Background(), nil, r, Options{Ponytail: Prompt{Enabled: false, Level: "full", Mode: "smart"}})
	if len(st.Skipped) != 0 {
		t.Fatalf("a disabled saver named itself: %v", st.Skipped)
	}
}

func TestRTKSaysSkippedWhenNoToolTextQualified(t *testing.T) {
	r := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("hi")})
	if st := Apply(context.Background(), nil, r, Options{RTK: true}); strings.Join(st.Skipped, ",") != "rtk" {
		t.Fatalf("skipped %v", st.Skipped)
	}
}

func TestNamesListsTheSaversThatActed(t *testing.T) {
	st := Stats{Saved: 12, Filters: []string{"html"}, Prompts: []string{"caveman"}, Headroom: true, Skipped: []string{"rtk"}}
	if got := st.Names(); got != "html,headroom,caveman" {
		t.Fatalf("Names = %q", got)
	}
	if got := (Stats{}).Names(); got != "" {
		t.Fatalf("an idle saver named %q", got)
	}
}

func TestTheStatsNameTheLevelEachPromptRanAtAndTheCustomLength(t *testing.T) {
	r := requestWith(translate.Message{Role: translate.RoleUser, Content: translate.TextPart("hello")})
	st := Apply(context.Background(), nil, r, Options{
		Caveman:  Prompt{Enabled: true, Level: "lite", Mode: "always"},
		Ponytail: Prompt{Enabled: true, Level: "ultra", Mode: "always"},
		Custom:   Custom{Enabled: true, Text: "  be brief  "},
	})
	if got := strings.Join(st.Levels, ","); got != "caveman=lite,ponytail=ultra" {
		t.Fatalf("levels %q", got)
	}
	if st.CustomChars != len("be brief") {
		t.Fatalf("custom length %d", st.CustomChars)
	}
}
