package plan

import (
	"slices"
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/text"
)

// maxPlanBytes bounds what one plan may add to a request.
const maxPlanBytes = 32 << 10

// planPrompt is the architect instruction, carried verbatim as prompt data.
const planPrompt = `You are a software architect. The user is requesting a non-trivial code implementation or architecture change.
Analyze the request and generate a concise, structured implementation plan.

Format your response STRICTLY inside <PLAN>...</PLAN> tags as follows:
<PLAN>
## Overview
Brief 1-2 sentence architecture summary.

## Proposed Changes
List of files to create/edit and their concrete responsibilities.

## Implementation Steps & Verification
1. Step 1 (Key functions / types / components)
2. Step 2 (Wiring & edge cases)
3. Self-check verification (tests or assertions)
</PLAN>

Rules:
- Output ONLY the <PLAN>...</PLAN> block.
- Keep it concise, high-signal, zero fluff. No explanations outside the tags.
- Focus on concrete file paths, API contracts, and key algorithms.`

// planBlock is the system turn a generated plan becomes.
func planBlock(planText string) string {
	return "The following architecture plan was generated for this implementation task:\n" +
		strings.TrimSpace(planText) + "\nImplement the code following this plan faithfully."
}

// PlanMessages is the planning conversation: the architect instruction, then
// the client's turns with its own system turns left out.
func PlanMessages(r *translate.Request) []translate.Message {
	out := make([]translate.Message, 0, len(r.Messages)+1)
	out = append(out, translate.Message{Role: translate.RoleSystem, Content: translate.TextPart(planPrompt)})
	for _, m := range r.Messages {
		if m.Role == translate.RoleSystem {
			continue
		}
		out = append(out, m)
	}
	return out
}

// Inject puts the plan in front of the last user turn, where the answer that
// follows reads it as guidance for that turn. A request with no user turn gets
// it at the top. The same plan is never inserted twice, so a retry or a second
// account cannot stack it.
func Inject(r *translate.Request, planText string) {
	planText = strings.TrimSpace(text.CutBytes(planText, maxPlanBytes))
	if planText == "" {
		return
	}
	block := planBlock(planText)
	for _, m := range r.Messages {
		if m.Role == translate.RoleSystem && m.Text() == block {
			return
		}
	}
	turn := translate.Message{Role: translate.RoleSystem, Content: translate.TextPart(block)}
	at := 0
	for i, m := range slices.Backward(r.Messages) {
		if m.Role == translate.RoleUser {
			at = i
			break
		}
	}
	out := make([]translate.Message, 0, len(r.Messages)+1)
	out = append(out, r.Messages[:at]...)
	out = append(out, turn)
	out = append(out, r.Messages[at:]...)
	r.Messages = out
}

// hasPlan reports whether the text already carries a plan marker.
func hasPlan(body string) bool {
	return strings.Contains(body, "<PLAN>") || strings.Contains(body, "</PLAN>")
}

// Combo is one stored combo as the picker needs it.
type Combo struct {
	ID      string
	Name    string
	Members []string
}

// Combos picks the planning combo and the combo the code should answer from.
// A manual choice counts when it names a stored combo with at least one member;
// otherwise the first combo holding a claude-named member plans, and failing
// that the first combo does. The code combo falls back to the combo the client
// itself named. An empty plan name means "do not plan".
func Combos(list []Combo, s Settings, requested string) (planCombo, codeCombo string) {
	planCombo = autoCombo(list)
	if s.Mode == "manual" && s.PlanCombo != "" {
		if c, ok := findCombo(list, s.PlanCombo); ok && len(c.Members) > 0 {
			planCombo = c.Name
		}
	}
	if s.Mode == "manual" && s.CodeCombo != "" {
		if c, ok := findCombo(list, s.CodeCombo); ok && len(c.Members) > 0 {
			codeCombo = c.Name
		}
	}
	if codeCombo == "" {
		if c, ok := findCombo(list, requested); ok {
			codeCombo = c.Name
		}
	}
	return planCombo, codeCombo
}

// autoCombo is the reference's automatic pick: the first combo that can reach
// a Claude model, else the first one that can reach anything.
func autoCombo(list []Combo) string {
	first := ""
	for _, c := range list {
		if len(c.Members) == 0 {
			continue
		}
		if first == "" {
			first = c.Name
		}
		for _, m := range c.Members {
			if strings.Contains(strings.ToLower(m), "claude") {
				return c.Name
			}
		}
	}
	return first
}

// findCombo matches a stored combo by id or by name, as the dashboard writes
// either.
func findCombo(list []Combo, ref string) (Combo, bool) {
	if ref == "" {
		return Combo{}, false
	}
	for _, c := range list {
		if c.ID == ref {
			return c, true
		}
	}
	for _, c := range list {
		if c.Name == ref {
			return c, true
		}
	}
	return Combo{}, false
}
