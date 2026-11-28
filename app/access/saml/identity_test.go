package saml

import (
	"strings"
	"testing"
)

func assertionWith(attrs []Attribute, nameID string) Assertion {
	return Assertion{
		Attributes: attrs,
		Subject:    Subject{NameID: NameID{Value: nameID}},
	}
}

func TestEmailPrefersTheConfiguredAttribute(t *testing.T) {
	a := assertionWith([]Attribute{
		{Name: "mail", Values: []string{"mail@example.com"}},
		{Name: "custom", Values: []string{"custom@example.com"}},
	}, "nameid@example.com")

	got, exact, _, ok := Email(a, Settings{AttributeEmail: "custom"})
	if !ok || exact || got != "custom@example.com" {
		t.Fatalf("%q %v %v", got, exact, ok)
	}
	got, exact, _, ok = Email(a, Settings{})
	if !ok || exact || got != "mail@example.com" {
		t.Fatalf("%q %v %v: the common spelling was not found", got, exact, ok)
	}
}

func TestEmailFallsBackToALookingNameIDAndMarksItExact(t *testing.T) {
	got, exact, _, ok := Email(assertionWith(nil, "ops@example.com"), Settings{})
	if !ok || !exact || got != "ops@example.com" {
		t.Fatalf("%q %v %v: a NameID address must be exact-only", got, exact, ok)
	}
	for _, bad := range []string{"", "no-at", "two@at@test", "@test", "user@", "with space@test"} {
		if _, _, _, ok := Email(assertionWith(nil, bad), Settings{}); ok {
			t.Errorf("%q became an address", bad)
		}
	}
}

func TestEmailIgnoresAnOverlongValue(t *testing.T) {
	long := strings.Repeat("a", 400) + "@example.com"
	if _, _, _, ok := Email(assertionWith([]Attribute{{Name: "mail", Values: []string{long}}}, ""), Settings{AttributeEmail: "mail"}); ok {
		t.Fatal("a value over 320 bytes became an address")
	}
}

func TestNamePrefersTheConfiguredAttributeThenCommonSpellings(t *testing.T) {
	a := assertionWith([]Attribute{
		{Name: "cn", Values: []string{"Commy"}},
		{Name: "displayName", Values: []string{"Display"}},
	}, "nameid@example.com")

	if got := Name(a, Settings{AttributeName: "displayName"}); got != "Display" {
		t.Fatalf("%q", got)
	}
	if got := Name(a, Settings{}); got != "Display" {
		t.Fatalf("%q: the first common spelling did not win", got)
	}
	if got := Name(assertionWith(nil, ""), Settings{}); got != "" {
		t.Fatalf("%q", got)
	}
	if got := Name(assertionWith(nil, "nameid@example.com"), Settings{}); got != "" {
		t.Fatalf("%q: the NameID became a full name", got)
	}
}

// An address from the operator's configured attribute is trusted; one that fell
// back to a common spelling is not, because Entra's `mail` carries a guest's
// user-editable address.
func TestAMailAttributeFromACommonSpellingIsUnproven(t *testing.T) {
	a := assertionWith([]Attribute{{Name: "mail", Values: []string{"guest@corp.test"}}}, "")
	if got, _, unproven, ok := Email(a, Settings{AttributeEmail: "custom"}); !ok || got != "guest@corp.test" || !unproven {
		t.Fatalf("%q unproven %v ok %v: a fallback spelling was trusted", got, unproven, ok)
	}
	if _, _, unproven, ok := Email(a, Settings{AttributeEmail: "mail"}); !ok || unproven {
		t.Fatalf("the configured attribute was not trusted: unproven %v ok %v", unproven, ok)
	}
}
