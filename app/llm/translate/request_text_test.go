package translate

import (
	"strings"
	"testing"
)

func turnsOf(roles ...Role) *Request {
	r := &Request{}
	for i, role := range roles {
		r.Messages = append(r.Messages, Message{Role: role, Content: TextPart(string(role) + string(rune('0'+i)))})
	}
	return r
}

func TestLastUserTextIsTheLastNonEmptyUserTurn(t *testing.T) {
	r := turnsOf(RoleSystem, RoleUser, RoleAssistant, RoleUser)
	if got := r.LastUserText(); got != "user3" {
		t.Fatalf("got %q", got)
	}
	r.Messages = append(r.Messages, Message{Role: RoleUser, Content: TextPart("  \n ")})
	if got := r.LastUserText(); got != "user3" {
		t.Fatalf("a blank turn hid the real one: %q", got)
	}
	r.Messages[3].Content = TextPart("  padded  ")
	if got := r.LastUserText(); got != "padded" {
		t.Fatalf("not trimmed: %q", got)
	}
	if got := turnsOf(RoleSystem).LastUserText(); got != "" {
		t.Fatalf("no user turn: %q", got)
	}
	if got := (*Request)(nil).LastUserText(); got != "" {
		t.Fatalf("nil request: %q", got)
	}
}

func TestAppendSystemAddsOnceAndKeepsTheFirstSystemTurn(t *testing.T) {
	r := turnsOf(RoleSystem, RoleUser)
	if !r.AppendSystem("A") || !r.AppendSystem("B") {
		t.Fatal("a new prompt was refused")
	}
	if r.AppendSystem("A") || r.AppendSystem("B") {
		t.Fatal("a prompt that is already a segment was added again")
	}
	if got := r.Messages[0].Text(); got != "system0\n\nA\n\nB" {
		t.Fatalf("system turn %q", got)
	}
	if len(r.Messages) != 2 {
		t.Fatalf("%d turns", len(r.Messages))
	}
}

func TestAppendSystemCreatesATurnAtTheTopWhenThereIsNone(t *testing.T) {
	r := turnsOf(RoleUser)
	if !r.AppendSystem("P") || len(r.Messages) != 2 || r.Messages[0].Role != RoleSystem || r.Messages[0].Text() != "P" {
		t.Fatalf("%+v", r.Messages)
	}
	if r.AppendSystem("") {
		t.Fatal("an empty prompt changed the request")
	}
}

func TestAppendSystemDoesNotMatchTextThatMerelyContainsThePrompt(t *testing.T) {
	r := turnsOf(RoleSystem, RoleUser)
	r.Messages[0].Content = TextPart("please " + strings.Repeat("x", 3) + " help")
	if !r.AppendSystem("xxx") {
		t.Fatal("a prompt that only appears inside other text was taken as present")
	}
}

func TestAppendSystemFillsAnEmptySystemTurn(t *testing.T) {
	r := &Request{Messages: []Message{{Role: RoleSystem}, {Role: RoleUser, Content: TextPart("hi")}}}
	if !r.AppendSystem("P") || r.Messages[0].Text() != "P" {
		t.Fatalf("%+v", r.Messages)
	}
}
