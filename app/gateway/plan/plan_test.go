package plan

import (
	"strings"
	"testing"

	"rllmx/app/llm/translate"
)

func conversation() *translate.Request {
	return &translate.Request{Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart("base prompt")},
		{Role: translate.RoleUser, Content: translate.TextPart("first turn")},
		{Role: translate.RoleAssistant, Content: translate.TextPart("first answer")},
		{Role: translate.RoleUser, Content: translate.TextPart("second turn")},
	}}
}

func TestThePlanPrecedesTheLastUserTurn(t *testing.T) {
	r := conversation()
	Inject(r, "<PLAN>\n1. Touch auth.js\n</PLAN>")
	if len(r.Messages) != 5 {
		t.Fatalf("%d turns", len(r.Messages))
	}
	if r.Messages[3].Role != translate.RoleSystem {
		t.Fatalf("the plan is not a system turn: %+v", r.Messages[3])
	}
	if !strings.Contains(r.Messages[3].Text(), "<PLAN>\n1. Touch auth.js\n</PLAN>") {
		t.Fatalf("plan turn %q", r.Messages[3].Text())
	}
	if r.Messages[4].Text() != "second turn" {
		t.Fatalf("the last turn moved: %q", r.Messages[4].Text())
	}
}

func TestAPlanIsInjectedOnce(t *testing.T) {
	r := conversation()
	Inject(r, "<PLAN>a</PLAN>")
	Inject(r, "<PLAN>a</PLAN>")
	if len(r.Messages) != 5 {
		t.Fatalf("a second plan was stacked: %d turns", len(r.Messages))
	}
}

func TestAnEmptyPlanChangesNothing(t *testing.T) {
	r := conversation()
	Inject(r, "  \n\t ")
	if len(r.Messages) != 4 {
		t.Fatalf("%d turns", len(r.Messages))
	}
	empty := &translate.Request{Messages: []translate.Message{{Role: translate.RoleSystem, Content: translate.TextPart("s")}}}
	Inject(empty, "<PLAN>p</PLAN>")
	if len(empty.Messages) != 2 || empty.Messages[0].Role != translate.RoleSystem || !hasPlan(empty.Messages[0].Text()) {
		t.Fatalf("a request without a user turn: %+v", empty.Messages)
	}
}

func TestThePlanIsBounded(t *testing.T) {
	r := conversation()
	Inject(r, strings.Repeat("p", maxPlanBytes*2))
	block := r.Messages[3].Text()
	if len(block) > maxPlanBytes+len(planBlock("")) {
		t.Fatalf("the plan block is %d bytes", len(block))
	}
}

func TestPlanMessagesCarryThePrompt(t *testing.T) {
	got := PlanMessages(conversation())
	if len(got) != 4 {
		t.Fatalf("%d turns", len(got))
	}
	if got[0].Role != translate.RoleSystem || got[0].Text() != planPrompt {
		t.Fatalf("first turn %+v", got[0])
	}
	for _, m := range got[1:] {
		if m.Role == translate.RoleSystem {
			t.Fatalf("a client system turn reached the planning call: %+v", m)
		}
	}
	if got[1].Text() != "first turn" {
		t.Fatalf("turns reordered: %+v", got)
	}
}

func TestHasPlan(t *testing.T) {
	if !hasPlan("<PLAN>a") || !hasPlan("b</PLAN>") || hasPlan("plain") {
		t.Fatal("hasPlan missed a marker")
	}
}

func TestCombos(t *testing.T) {
	list := []Combo{
		{ID: "c0", Name: "empty"},
		{ID: "c1", Name: "fast", Members: []string{"openai/gpt-5"}},
		{ID: "c2", Name: "claude-vip", Members: []string{"anthropic/claude-sonnet-5"}},
		{ID: "c3", Name: "code", Members: []string{"openai/gpt-6"}},
	}
	cases := map[string]struct {
		settings Settings
		asked    string
		plan     string
		code     string
	}{
		"auto takes the claude combo": {Settings{Mode: "auto"}, "fast", "claude-vip", "fast"},
		"manual by id":                {Settings{Mode: "manual", PlanCombo: "c3", CodeCombo: "c1"}, "fast", "code", "fast"},
		"manual by name":              {Settings{Mode: "manual", PlanCombo: "fast"}, "claude-vip", "fast", "claude-vip"},
		"manual missing falls to auto": {
			Settings{Mode: "manual", PlanCombo: "gone", CodeCombo: "gone"}, "claude-vip", "claude-vip", "claude-vip",
		},
		"a manual choice with no members is ignored": {
			Settings{Mode: "manual", PlanCombo: "empty"}, "code", "claude-vip", "code",
		},
		"no claude combo": {
			Settings{Mode: "auto"}, "x",
			"fast", "",
		},
		"no configured code combo keeps the requested one": {Settings{Mode: "manual", PlanCombo: "c3"}, "c3", "code", "code"},
		"an empty list does nothing":                       {Settings{Mode: "auto"}, "fast", "", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := list
			if name == "no claude combo" {
				in = []Combo{{ID: "c1", Name: "fast", Members: []string{"openai/gpt-5"}}}
			}
			if name == "an empty list does nothing" {
				in = nil
			}
			p, code := Combos(in, c.settings, c.asked)
			if p != c.plan || code != c.code {
				t.Fatalf("plan %q code %q, want %q %q", p, code, c.plan, c.code)
			}
		})
	}
}

func FuzzPlanText(f *testing.F) {
	f.Add("<PLAN>a</PLAN> text")
	f.Add("</PLAN><PLAN>x")
	f.Add("plain")
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 4*maxPlanBytes {
			return
		}
		if strings.TrimSpace(body) == "" {
			return
		}
		r := conversation()
		Inject(r, body)
		if len(r.Messages) != 5 {
			t.Fatalf("a plan was lost on %q", body)
		}
		if !strings.HasSuffix(r.Messages[3].Text(), "Implement the code following this plan faithfully.") {
			t.Fatalf("the plan turn is malformed: %q", r.Messages[3].Text())
		}
	})
}
