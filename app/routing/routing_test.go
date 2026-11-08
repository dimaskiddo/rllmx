package routing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"rllmx/app/repository"
	"rllmx/app/upstream"
	"rllmx/tests/store"
)

type fakeSource struct {
	aliases map[string]string
	nodes   []Node
	combos  []string
	err     error
	reads   int
}

func (f *fakeSource) Aliases(context.Context) (map[string]string, error) {
	f.reads++
	return f.aliases, f.err
}
func (f *fakeSource) Nodes(context.Context) ([]Node, error)        { f.reads++; return f.nodes, f.err }
func (f *fakeSource) ComboNames(context.Context) ([]string, error) { f.reads++; return f.combos, f.err }

func resolver(t *testing.T, src *fakeSource) *Resolver {
	t.Helper()
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	return &Resolver{Registry: reg, Snapshot: NewSnapshot(src, nil)}
}

func resolve(t *testing.T, r *Resolver, in string) Target {
	t.Helper()
	got, err := r.Resolve(context.Background(), in)
	if err != nil {
		t.Fatalf("%q: %v", in, err)
	}
	return got
}

func TestSuffixOrderDoesNotMatter(t *testing.T) {
	r := resolver(t, &fakeSource{})
	a := resolve(t, r, "claude-opus-5[1m](high)")
	b := resolve(t, r, "claude-opus-5(high)[1m]")
	c := resolve(t, r, "claude-opus-5[1M][1m](high)")
	if a != b || a != c {
		t.Fatalf("%+v %+v %+v", a, b, c)
	}
	if a.Model != "claude-opus-5" || !a.ContextMarker || a.Thinking.Level != "high" || a.Provider != "anthropic" {
		t.Fatalf("%+v", a)
	}
}

func TestThinkingValues(t *testing.T) {
	for in, want := range map[string]Thinking{
		"m(none)": {Mode: ThinkingNone}, "m(OFF)": {Mode: ThinkingNone}, "m(auto)": {Mode: ThinkingAuto},
		"m(xhigh)": {Mode: ThinkingLevel, Level: "xhigh"}, "m(8192)": {Mode: ThinkingBudget, Budget: 8192},
		"m(1000000)": {Mode: ThinkingBudget, Budget: 1_000_000},
		"m(1000001)": {}, "m(0)": {}, "m(-5)": {}, "m(0x10)": {}, "m(007)": {}, "m(banana)": {}, "m()": {},
	} {
		name, _, got := splitSuffixes(in)
		if got != want || name != "m" {
			t.Errorf("%q: name %q thinking %+v, want %+v", in, name, got, want)
		}
	}
}

func TestAnUnknownThinkingValueIsDroppedWithoutAnOverride(t *testing.T) {
	got := resolve(t, resolver(t, &fakeSource{}), "gpt-4o(banana)")
	if got.Thinking.Set() || got.Model != "gpt-4o" {
		t.Fatalf("%+v", got)
	}
}

func TestProviderPrefixAndAliases(t *testing.T) {
	r := resolver(t, &fakeSource{})
	for in, want := range map[string][2]string{
		"cc/claude-opus-5":              {"claude", "claude-opus-5"},
		"cx/gpt-6-sol":                  {"codex", "gpt-6-sol"},
		"openrouter/anthropic/claude-x": {"openrouter", "anthropic/claude-x"},
		"unknown-prefix/some-model":     {"unknown-prefix", "some-model"},
		"el/voice":                      {"elevenlabs", "voice"},
	} {
		got := resolve(t, r, in)
		if got.Provider != want[0] || got.Model != want[1] {
			t.Errorf("%q: %+v", in, got)
		}
	}
}

func TestInference(t *testing.T) {
	r := resolver(t, &fakeSource{})
	for in, want := range map[string]string{
		"codex-auto-review": "codex", "gpt-5.6-sol": "codex", "gpt-6-luna": "codex", "gpt-daybreak-blue": "codex", "gpt-reserve": "codex",
		"claude-opus-5": "anthropic", "us.anthropic.claude-x": "bedrock", "eu.meta.llama": "bedrock", "gemini-2.5-pro": "gemini",
		"gpt-4o": "openai", "o3-mini": "openai", "o1": "openai", "deepseek-chat": "openrouter", "mystery-model": "openai",
		"GPT-5.2": "codex", "CLAUDE-OPUS": "anthropic",
	} {
		if got := resolve(t, r, in); got.Provider != want {
			t.Errorf("%q: %q, want %q", in, got.Provider, want)
		}
	}
}

func TestAComboNameBeatsAnAlias(t *testing.T) {
	src := &fakeSource{combos: []string{"fast"}, aliases: map[string]string{"fast": `"openai/gpt-4o"`}}
	got := resolve(t, resolver(t, src), "fast")
	if got.Combo != "fast" || got.Provider != "" {
		t.Fatalf("%+v", got)
	}
}

func TestAComboNameWithASlashIsNeverReached(t *testing.T) {
	src := &fakeSource{combos: []string{"a/b"}}
	got := resolve(t, resolver(t, src), "a/b")
	if got.Combo != "" || got.Provider != "a" || got.Model != "b" {
		t.Fatalf("%+v", got)
	}
}

func TestUserAliasShapes(t *testing.T) {
	src := &fakeSource{aliases: map[string]string{
		"s": `"cc/claude-opus-5"`, "o": `{"provider":"cx","model":"gpt-6-sol"}`,
		"bad1": `"no-slash"`, "bad2": `{"provider":"x"}`, "bad3": `42`, "bad4": `not json`, "bad5": `"/model"`,
	}}
	r := resolver(t, src)
	if g := resolve(t, r, "s"); g.Provider != "claude" || g.Model != "claude-opus-5" {
		t.Errorf("%+v", g)
	}
	if g := resolve(t, r, "o"); g.Provider != "codex" || g.Model != "gpt-6-sol" {
		t.Errorf("%+v", g)
	}
	for _, n := range []string{"bad1", "bad2", "bad3", "bad4", "bad5"} {
		if g := resolve(t, r, n); g.Model != n || g.Provider != "openai" {
			t.Errorf("%s: a bad alias was used: %+v", n, g)
		}
	}
}

func TestAliasesDoNotChain(t *testing.T) {
	src := &fakeSource{aliases: map[string]string{"a": `"cc/b"`, "b": `"openai/gpt-4o"`, "loop": `"loop/loop"`}}
	r := resolver(t, src)
	if g := resolve(t, r, "a"); g.Provider != "claude" || g.Model != "b" {
		t.Fatalf("the alias result was looked up again: %+v", g)
	}
	if g := resolve(t, r, "loop"); g.Provider != "loop" || g.Model != "loop" {
		t.Fatalf("%+v", g)
	}
}

func TestTheBuiltInAliasAndTheUserAliasOrder(t *testing.T) {
	r := resolver(t, &fakeSource{})
	if g := resolve(t, r, "grok-build"); g.Provider != "grok-cli" || g.Model != "grok-build" {
		t.Fatalf("%+v", g)
	}
	user := resolver(t, &fakeSource{aliases: map[string]string{"grok-build": `"openai/gpt-4o"`}})
	if g := resolve(t, user, "grok-build"); g.Provider != "openai" {
		t.Fatalf("the built-in alias beat the user's: %+v", g)
	}
}

func TestNodePrefixes(t *testing.T) {
	src := &fakeSource{nodes: []Node{
		{ID: "n-oa", Type: "openai-compatible", Prefix: "mine"},
		{ID: "n-an", Type: "anthropic-compatible", Prefix: "ant"},
		{ID: "n-em", Type: "custom-embedding", Prefix: "emb"},
		{ID: "n-none", Type: "openai-compatible", Prefix: ""},
		{ID: "n-steal", Type: "openai-compatible", Prefix: "cc"},
		{ID: "n-also", Type: "openai-compatible", Prefix: "polly"},
		{ID: "n-both", Type: "custom-embedding", Prefix: "dup"},
		{ID: "n-both-oa", Type: "openai-compatible", Prefix: "dup"},
	}}
	r := resolver(t, src)
	for in, want := range map[string]string{
		"mine/m": "n-oa", "ant/m": "n-an", "emb/m": "n-em", "dup/m": "n-both-oa",
		"cc/m": "claude", "polly/m": "aws-polly", "other/m": "other",
	} {
		if g := resolve(t, r, in); g.Provider != want {
			t.Errorf("%q: %q, want %q", in, g.Provider, want)
		}
	}
}

func TestANodeWithAnEmptyPrefixMatchesNothing(t *testing.T) {
	src := &fakeSource{nodes: []Node{{ID: "n", Type: "openai-compatible", Prefix: ""}}}
	if _, err := resolver(t, src).Resolve(context.Background(), "/model"); !errors.Is(err, ErrEmpty) {
		t.Fatalf("err %v", err)
	}
}

func TestInputLimits(t *testing.T) {
	r := resolver(t, &fakeSource{})
	ctx := context.Background()
	if _, err := r.Resolve(ctx, strings.Repeat("a", 257)); !errors.Is(err, ErrTooLong) {
		t.Errorf("long: %v", err)
	}
	if _, err := r.Resolve(ctx, strings.Repeat("a", 256)); err != nil {
		t.Errorf("256 bytes: %v", err)
	}
	for _, in := range []string{"gpt\x00-4", "gpt\n4", "a\u0085b", "a\x7fb"} {
		if _, err := r.Resolve(ctx, in); !errors.Is(err, ErrBadInput) {
			t.Errorf("%q: %v", in, err)
		}
	}
	for _, in := range []string{"", "   ", "[1m]", "(high)", "a/", "/a"} {
		if _, err := r.Resolve(ctx, in); !errors.Is(err, ErrEmpty) {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestAFailedReadIsAnErrorAndIsNotCached(t *testing.T) {
	src := &fakeSource{err: errors.New("disk")}
	r := resolver(t, src)
	if _, err := r.Resolve(context.Background(), "gpt-4o"); err == nil {
		t.Fatal("a failed read became a guess")
	}
	src.err = nil
	if _, err := r.Resolve(context.Background(), "gpt-4o"); err != nil {
		t.Fatalf("the failure was cached: %v", err)
	}
}

func TestTheSnapshotReusesReadsAndInvalidates(t *testing.T) {
	src := &fakeSource{}
	now := time.Unix(1000, 0)
	snap := NewSnapshot(src, func() time.Time { return now })
	ctx := context.Background()
	for range 3 {
		if _, err := snap.Aliases(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if src.reads != 1 {
		t.Fatalf("%d reads", src.reads)
	}
	now = now.Add(SnapshotTTL - time.Millisecond)
	_, _ = snap.Aliases(ctx)
	if src.reads != 1 {
		t.Fatalf("expired early: %d reads", src.reads)
	}
	now = now.Add(time.Millisecond)
	_, _ = snap.Aliases(ctx)
	if src.reads != 2 {
		t.Fatalf("not refreshed at the TTL: %d reads", src.reads)
	}
	snap.Invalidate()
	_, _ = snap.Aliases(ctx)
	if src.reads != 3 {
		t.Fatalf("Invalidate did not drop the read: %d reads", src.reads)
	}
}

func TestTheRepositorySourceReadsRealRows(t *testing.T) {
	st := store.Migrated(t)
	ctx := context.Background()
	if err := st.KV().Set(ctx, repository.ScopeModelAliases, "mine", `"cc/claude-opus-5"`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Combos().Create(ctx, "fast", []string{"openai/gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Nodes().CreateUnique(ctx, repository.Node{Type: "openai-compatible", Name: "n", Data: `{"prefix":"mine2"}`}, "mine2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Nodes().CreateUnique(ctx, repository.Node{Type: "openai-compatible", Name: "bad", Data: `{"prefix":7}`}, ""); err != nil {
		t.Fatal(err)
	}
	reg, _ := upstream.Default()
	r := &Resolver{Registry: reg, Snapshot: NewSnapshot(NewRepositorySource(st.Store), nil)}
	if g := resolve(t, r, "mine"); g.Provider != "claude" {
		t.Errorf("alias: %+v", g)
	}
	if g := resolve(t, r, "fast"); g.Combo != "fast" {
		t.Errorf("combo: %+v", g)
	}
	if g := resolve(t, r, "mine2/m"); g.Provider == "mine2" || g.Model != "m" {
		t.Errorf("node: %+v", g)
	}
}
