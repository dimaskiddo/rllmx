package routing

import (
	"context"
	"errors"
	"testing"

	"rllmx/app/store/repository"
)

// combos is a resolver over a hand-written combo table.
func combos(t *testing.T, table map[string][]string) *Resolver {
	t.Helper()
	set := map[string]repository.Combo{}
	names := make([]string, 0, len(table))
	for name, members := range table {
		set[name] = repository.Combo{Name: name, Models: members}
		names = append(names, name)
	}
	return resolver(t, &fakeSource{combos: names, comboSet: set})
}

func expand(t *testing.T, r *Resolver, name string) []Member {
	t.Helper()
	got, err := r.Expand(context.Background(), name)
	if err != nil {
		t.Fatalf("Expand(%s): %v", name, err)
	}
	return got
}

func TestAComboExpandsInDeclaredOrder(t *testing.T) {
	r := combos(t, map[string][]string{"fast": {"openai/gpt-4o", "claude/claude-opus-5", "openai/gpt-4o-mini"}})
	got := expand(t, r, "fast")
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	if got[0].Provider != "openai" || got[0].Model != "gpt-4o" || got[1].Provider != "claude" || got[2].Model != "gpt-4o-mini" {
		t.Fatalf("%+v", got)
	}
}

func TestANestedComboIsFlattenedInPlace(t *testing.T) {
	r := combos(t, map[string][]string{
		"outer": {"openai/gpt-4o", "inner", "claude/claude-opus-5"},
		"inner": {"openai/o3", "openai/o4"},
	})
	got := expand(t, r, "outer")
	if len(got) != 4 || got[1].Model != "o3" || got[2].Model != "o4" || got[3].Provider != "claude" {
		t.Fatalf("%+v", got)
	}
}

func TestACycleIsRefusedRatherThanFollowed(t *testing.T) {
	r := combos(t, map[string][]string{
		"a": {"openai/gpt-4o", "b"},
		"b": {"openai/gpt-4o-mini", "a"},
	})
	if _, err := r.Expand(context.Background(), "a"); !errors.Is(err, ErrComboCycle) {
		t.Fatalf("err = %v", err)
	}
	// A combo that names itself directly is the same refusal.
	self := combos(t, map[string][]string{"loop": {"openai/gpt-4o", "loop"}})
	if _, err := self.Expand(context.Background(), "loop"); !errors.Is(err, ErrComboCycle) {
		t.Fatalf("self err = %v", err)
	}
}

func TestNestingIsBounded(t *testing.T) {
	// Four levels, at the limit, expand; five are refused.
	ok := combos(t, map[string][]string{
		"a": {"b"}, "b": {"c"}, "c": {"d"}, "d": {"openai/gpt-4o"},
	})
	if got := expand(t, ok, "a"); len(got) != 1 || got[0].Model != "gpt-4o" {
		t.Fatalf("%+v", got)
	}
	deep := combos(t, map[string][]string{
		"a": {"b"}, "b": {"c"}, "c": {"d"}, "d": {"e"}, "e": {"openai/gpt-4o"},
	})
	if _, err := deep.Expand(context.Background(), "a"); !errors.Is(err, ErrComboDepth) {
		t.Fatalf("err = %v", err)
	}
}

func TestAMemberThatDoesNotResolveIsSkipped(t *testing.T) {
	r := combos(t, map[string][]string{"c": {"", "  ", "bad\x00name", "openai/gpt-4o"}})
	got := expand(t, r, "c")
	if len(got) != 1 || got[0].Provider != "openai" {
		t.Fatalf("%+v", got)
	}
}

func TestAComboWithNoReachableMemberIsRefused(t *testing.T) {
	r := combos(t, map[string][]string{"empty": {"", "   "}})
	if _, err := r.Expand(context.Background(), "empty"); !errors.Is(err, ErrComboEmpty) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Expand(context.Background(), "missing"); !errors.Is(err, ErrComboEmpty) {
		t.Fatalf("an unknown name: %v", err)
	}
}

func TestABareMemberResolvesThroughTheSameRulesAsARequest(t *testing.T) {
	// An alias member expands to what a request for that alias would reach.
	r := resolver(t, &fakeSource{
		combos:   []string{"c"},
		comboSet: map[string]repository.Combo{"c": {Name: "c", Models: []string{"cm"}}},
		aliases:  map[string]string{"cm": `"claude/claude-x"`},
	})
	got := expand(t, r, "c")
	if len(got) != 1 || got[0].Provider != "claude" || got[0].Model != "claude-x" {
		t.Fatalf("%+v", got)
	}
}
