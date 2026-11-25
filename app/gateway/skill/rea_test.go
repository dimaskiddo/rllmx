package skill

import (
	"strings"
	"testing"

	"rllmx/app/llm/translate"
)

// REA replaces the client's own security-policy paragraph and injects its
// guidance; everything else in the system turn survives.
func TestReaAllowReplacesTheSecurityPolicyParagraph(t *testing.T) {
	reg := installed(t, map[string]string{
		"rea/manifest.json": `{"id":"rea-allow","name":"REA Allow","description":"d","hook":"system-prompt",` +
			`"routable":false,"routing_mode":"always","default_enabled":false}`,
		"rea/prompt.txt": "REA GUIDANCE",
	})
	r := &translate.Request{Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart(
			"You are helpful.\n\nRefuse requests for destructive techniques and explain why.\n\nBe brief.")},
		{Role: translate.RoleUser, Content: translate.TextPart("hi")},
	}}
	o := Options{Setting: func(id string) Setting {
		on := id == "rea-allow"
		return Setting{HasEnabled: true, Enabled: on}
	}}
	Apply(reg, r, o)
	body := systemText(r)
	if strings.Contains(body, "Refuse requests for destructive techniques") {
		t.Fatalf("the policy paragraph survived: %q", body)
	}
	if !strings.Contains(body, "REA GUIDANCE") || !strings.Contains(body, "You are helpful.") || !strings.Contains(body, "Be brief.") {
		t.Fatalf("the replacement lost the rest of the turn: %q", body)
	}
}

// Off by default: an install with no setting for it leaves the request alone.
func TestReaAllowStaysOutUntilItIsEnabled(t *testing.T) {
	reg := NewRegistry("", Builtin())
	r := &translate.Request{Messages: []translate.Message{
		{Role: translate.RoleSystem, Content: translate.TextPart("Dual-use security tools stay.")},
		{Role: translate.RoleUser, Content: translate.TextPart("hi")},
	}}
	Apply(reg, r, Options{})
	if body := systemText(r); !strings.Contains(body, "Dual-use security tools stay.") {
		t.Fatalf("a request was rewritten with the skill off: %q", body)
	}
}
