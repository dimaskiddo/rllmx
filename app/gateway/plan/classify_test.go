package plan

import (
	"context"
	"math"
	"strings"
	"testing"

	"rllmx/app/llm/translate"
	"rllmx/app/store/document"
)

// ask builds a request whose last user turn is body.
func ask(body string) *translate.Request {
	return &translate.Request{Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart("you are helpful")},
		{Role: translate.RoleUser, Content: translate.TextPart(body)},
	}}
}

func enabled() Settings { return Settings{Enabled: true, Threshold: 6} }

func TestClassifySkipsWhatCannotNeedAPlan(t *testing.T) {
	cases := map[string]struct {
		request *translate.Request
		reason  string
	}{
		"short ask":                       {ask("fix this typo"), "too_short"},
		"a question":                      {ask("how does the cache work under concurrency in this router"), "exclusion_pattern"},
		"build word but still a question": {ask("explain how to build an auth service with a database"), "exclusion_pattern"},
		"already planned": {&translate.Request{Messages: []translate.Message{
			{Role: translate.RoleUser, Content: translate.TextPart("<PLAN>do it</PLAN> build the module")},
		}}, "already_planned"},
		"tool loop": {&translate.Request{Messages: []translate.Message{
			{Role: translate.RoleUser, Content: translate.TextPart("build the module across several files")},
			{Role: translate.RoleTool, Content: translate.TextPart("ok")},
		}}, "agent_tool_loop"},
		"a tool call": {&translate.Request{Messages: []translate.Message{
			{Role: translate.RoleAssistant, ToolCalls: []translate.ToolCall{{ID: "1", Name: "f"}}},
			{Role: translate.RoleUser, Content: translate.TextPart("build the module across several files")},
		}}, "agent_tool_loop"},
		"no user turn": {&translate.Request{Messages: []translate.Message{
			{Role: translate.RoleSystem, Content: translate.TextPart("system only")},
		}}, "no_user_turn"},
		"no messages": {&translate.Request{}, "empty_messages"},
		"disabled":    {ask("build an auth service across multiple files with database and tokens"), "disabled"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := enabled()
			if name == "disabled" {
				s.Enabled = false
			}
			got := Classify(c.request, s)
			if got.NeedsPlan || got.Reason != c.reason {
				t.Fatalf("verdict %+v, want reason %q", got, c.reason)
			}
		})
	}
}

func TestClassifyScoresABuildTask(t *testing.T) {
	cases := map[string]struct {
		body  string
		plan  bool
		score float64
	}{
		"english build across files": {
			"build a full auth service across multiple files with database and tokens", true, 0.95,
		},
		"indonesian build": {
			"tolong buatkan sistem aplikasi baru dengan beberapa file dan juga database", true, 0.8,
		},
		"one scope, no synergy": {
			"build an endpoint that returns the cached answer quickly and safely please", false, 0.55,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := Classify(ask(c.body), enabled())
			if got.NeedsPlan != c.plan || math.Abs(got.Score-c.score) > 1e-9 {
				t.Fatalf("verdict %+v, want plan=%t score=%v", got, c.plan, c.score)
			}
		})
	}
}

func TestTheThresholdRaisesTheBar(t *testing.T) {
	body := "build a full auth service across multiple files with database and tokens"
	loose := enabled()
	loose.Threshold = 10
	if got := Classify(ask(body), loose); got.NeedsPlan || got.Reason != "below_threshold" {
		t.Fatalf("threshold 10 planned: %+v", got)
	}
	tight := enabled()
	tight.Threshold = 1
	if got := Classify(ask("refactor the module loader so it reports every failure with a clear error"), tight); !got.NeedsPlan {
		t.Fatalf("threshold 1 did not plan a build word: %+v", got)
	}
}

func TestClassifyBoundsItsScan(t *testing.T) {
	// The only build word sits past the scan bound, so only the length is seen.
	body := strings.Repeat("x ", maxScan/2+100) + "build"
	if got := Classify(ask(body), enabled()); math.Abs(got.Score-0.45) > 1e-9 {
		t.Fatalf("the tail past the scan bound was read: %+v", got)
	}
}

func TestReadClampsAndDefaults(t *testing.T) {
	cases := map[string]struct {
		row  string
		want Settings
	}{
		"empty":                {`{}`, Settings{Mode: "auto", Threshold: 6, MaxTokens: 800, TimeoutMS: 15000}},
		"manual":               {`{"autoPlanEnabled":true,"autoPlanMode":"manual","autoPlanComboId":"c1","autoCodeComboId":"c2","autoPlanSmartClassify":true}`, Settings{Enabled: true, Mode: "manual", PlanCombo: "c1", CodeCombo: "c2", Threshold: 6, MaxTokens: 800, TimeoutMS: 15000, SmartClassify: true}},
		"unknown mode is auto": {`{"autoPlanMode":"weird"}`, Settings{Mode: "auto", Threshold: 6, MaxTokens: 800, TimeoutMS: 15000}},
		"low clamp":            {`{"autoPlanComplexityThreshold":0,"autoPlanMaxTokens":1,"autoPlanTimeoutMs":0}`, Settings{Mode: "auto", Threshold: 1, MaxTokens: 200, TimeoutMS: 1000}},
		"high clamp":           {`{"autoPlanComplexityThreshold":42,"autoPlanMaxTokens":99999,"autoPlanTimeoutMs":999999}`, Settings{Mode: "auto", Threshold: 10, MaxTokens: 4000, TimeoutMS: 60000}},
		"combo too long":       {`{"autoPlanComboId":"` + strings.Repeat("a", comboRefMax+1) + `"}`, Settings{Mode: "auto", Threshold: 6, MaxTokens: 800, TimeoutMS: 15000}},
		"combo with control":   {`{"autoCodeComboId":"a\nb"}`, Settings{Mode: "auto", Threshold: 6, MaxTokens: 800, TimeoutMS: 15000}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Read(document.From(c.row)); got != c.want {
				t.Fatalf("settings %+v, want %+v", got, c.want)
			}
		})
	}
}

// ambiguous is a turn whose heuristic score sits close enough to the threshold
// that the classifier is unsure.
const ambiguous = "please refactor and implement the module loader with clearer errors over the whole tree"

func TestDecideAsksTheModelWhenUnsure(t *testing.T) {
	s := enabled()
	s.SmartClassify = true
	v := Classify(ask(ambiguous), s)
	if v.Confidence >= smartConfidence {
		t.Fatalf("the fixture is not ambiguous: %+v", v)
	}
	var asked string
	yes := Decide(context.Background(), ask(ambiguous), s, func(_ context.Context, q string) (string, bool) {
		asked = q
		return "YES", true
	})
	if !yes.NeedsPlan || yes.Reason != "tier2" {
		t.Fatalf("verdict %+v", yes)
	}
	if !strings.Contains(asked, "Task: \"please refactor") {
		t.Fatalf("question %q", asked)
	}
	if got := Decide(context.Background(), ask(ambiguous), s, func(context.Context, string) (string, bool) { return "NO", true }); got.NeedsPlan || got.Reason != "tier2" {
		t.Fatalf("a NO verdict %+v", got)
	}
}

func TestDecideKeepsTheHeuristicWhenTheJudgeFails(t *testing.T) {
	s := enabled()
	s.SmartClassify = true
	base := Classify(ask(ambiguous), s)
	if got := Decide(context.Background(), ask(ambiguous), s, func(context.Context, string) (string, bool) { return "", false }); got != base {
		t.Fatalf("verdict %+v, want %+v", got, base)
	}
}

func TestDecideAsksNothingWhenTheSettingIsOff(t *testing.T) {
	calls := 0
	judge := func(context.Context, string) (string, bool) { calls++; return "YES", true }
	Decide(context.Background(), ask(ambiguous), enabled(), judge)
	if calls != 0 {
		t.Fatalf("the judge was asked %d times", calls)
	}
	sure := enabled()
	sure.SmartClassify = true
	Decide(context.Background(), ask("build a full auth service across multiple files with database and tokens"), sure, judge)
	if calls != 0 {
		t.Fatalf("a confident verdict was re-asked %d times", calls)
	}
}

func TestQuestionCutsTheTurn(t *testing.T) {
	q := Question(strings.Repeat("a", 400))
	if !strings.HasSuffix(q, "\"") || strings.Contains(q, strings.Repeat("a", 301)) {
		t.Fatalf("question %q", q)
	}
}
