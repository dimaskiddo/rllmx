package routing

import (
	"context"
	"errors"
	"testing"

	"rllmx/app/llm/upstream"
)

func allow(t *testing.T, entries ...string) Allow {
	t.Helper()
	a, err := NewAllow(context.Background(), resolver(t, &fakeSource{combos: []string{"fast"}}), registry(t), entries)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAListedModelIsAllowedInAnyFormTheResolverUnderstands(t *testing.T) {
	r := resolver(t, &fakeSource{combos: []string{"fast"}})
	a, err := NewAllow(context.Background(), r, registry(t), []string{
		"openai/gpt-4o",               // provider/model
		"claude/claude-opus-5",        // another provider
		"openai/gpt-4o-mini",          // a second model of one provider
		" cx/gpt-5",                   // an alias prefix, space-trimmed
		"fast",                        // a combo name
		"unknown/model-that-resolves", // resolves by inference
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []string{"openai/gpt-4o", "claude/claude-opus-5", "openai/gpt-4o-mini", "codex/gpt-5"} {
		if got := resolve(t, r, in); !a.Target(got) {
			t.Errorf("%s should be allowed: %+v", in, got)
		}
	}
	if got := resolve(t, r, "fast"); !a.Target(got) {
		t.Errorf("the combo name should be allowed: %+v", got)
	}
}

func TestAModelThatIsNotListedIsRefused(t *testing.T) {
	a := allow(t, "openai/gpt-4o")
	r := resolver(t, &fakeSource{})
	for _, in := range []string{"openai/gpt-4o-mini", "claude/claude-opus-5", "openai/o3"} {
		if a.Target(resolve(t, r, in)) {
			t.Errorf("%s was allowed by a list of openai/gpt-4o", in)
		}
	}
}

func TestTheComparisonIsExact(t *testing.T) {
	// The resolver's answer is the identity; a lowercased lookalike may route to
	// a different upstream model, so it must not match.
	a := allow(t, "openai/GPT-4o")
	r := resolver(t, &fakeSource{})
	if !a.Target(resolve(t, r, "openai/GPT-4o")) {
		t.Fatal("the exact spelling was refused")
	}
	if a.Target(resolve(t, r, "openai/gpt-4o")) {
		t.Fatal("a differently cased model matched")
	}
	if a.Target(resolve(t, r, "openai/GPT-4O")) {
		t.Fatal("a differently cased model matched")
	}
}

func TestAComboIsMatchedByNameAndAModelIsNot(t *testing.T) {
	a := allow(t, "fast")
	r := resolver(t, &fakeSource{combos: []string{"fast"}})
	if !a.Target(resolve(t, r, "fast")) {
		t.Fatal("the combo name was refused")
	}
	// A model of the same spelling is a different target when no combo exists.
	plain := resolver(t, &fakeSource{})
	if a.Target(resolve(t, plain, "fast")) {
		t.Fatal("a model named like a combos was allowed by a combo entry")
	}
}

func TestAProviderEntryAllowsAProviderOnlyRoute(t *testing.T) {
	a := allow(t, "serper", "openai/gpt-4o")
	if !a.Provider("serper") {
		t.Fatal("a bare provider entry does not allow that provider")
	}
	if a.Provider("tavily") {
		t.Fatal("an unlisted provider was allowed")
	}
	// A provider entry does not open every model of it: only the provider-only
	// routes read this set.
	r := resolver(t, &fakeSource{})
	if a.Target(resolve(t, r, "openai/gpt-4o-mini")) {
		t.Fatal("a provider entry allowed a model")
	}
}

func TestAnEmptyListAllowsNothing(t *testing.T) {
	a := allow(t)
	r := resolver(t, &fakeSource{})
	if a.Target(resolve(t, r, "openai/gpt-4o")) || a.Provider("openai") {
		t.Fatal("an empty list allowed something")
	}
}

func TestAnEntryThatDoesNotResolveAllowsNothing(t *testing.T) {
	// Long, empty and control-carrying names are refused by the resolver; they
	// are skipped rather than failing the request.
	a := allow(t, "", "   ", "-----", "bad\x00name")
	r := resolver(t, &fakeSource{})
	if a.Target(resolve(t, r, "openai/gpt-4o")) {
		t.Fatal("a refused entry allowed a model")
	}
}

func TestAStorageErrorFailsTheListClosed(t *testing.T) {
	boom := errors.New("snapshot is down")
	r := resolver(t, &fakeSource{err: boom})
	// A bare entry reads the combo list, so the storage fault surfaces; a
	// prefixed one never touches the snapshot.
	if _, err := NewAllow(context.Background(), r, registry(t), []string{"gpt-4o"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the storage error", err)
	}
}

// registry is the real provider table, for the provider set.
func registry(t *testing.T) *upstream.Registry {
	t.Helper()
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
