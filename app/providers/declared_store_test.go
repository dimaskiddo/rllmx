package providers

import (
	"context"
	"strings"
	"testing"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

func TestALearnedFlagGoesIntoAnExistingCustomRowOnly(t *testing.T) {
	s := store.Migrated(t)
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const row = `{"providerAlias":"cc","id":"claude-custom","type":"llm","name":"C","caps":{"reasoning":true}}`
	if err := s.Store.KV().Set(ctx, repository.ScopeCustomModels, "cc|claude-custom|llm", row); err != nil {
		t.Fatal(err)
	}
	d := declaredStore{store: s.Store, reg: reg, caps: caps}
	on := true
	if err := d.Remember(ctx, "claude", "claude-custom", upstream.Declared{Vision: &on}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Store.KV().Get(ctx, repository.ScopeCustomModels, "cc|claude-custom|llm")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if !strings.Contains(got, `"vision":true`) || !strings.Contains(got, `"reasoning":true`) || !strings.Contains(got, `"name":"C"`) {
		t.Fatalf("the row reads %s", got)
	}
	if !caps.For("claude", "claude-custom").Vision {
		t.Fatal("the learned flag did not reach the resolver")
	}
	if err := d.Remember(ctx, "claude", "claude-opus-5", upstream.Declared{Vision: &on}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Store.KV().Get(ctx, repository.ScopeCustomModels, "cc|claude-opus-5|llm"); ok {
		t.Fatal("a built-in model was given a custom row")
	}
}
