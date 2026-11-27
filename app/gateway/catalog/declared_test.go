package catalog

import (
	"context"
	"errors"
	"testing"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

func TestLoadDeclaredGivesTheResolverTheStoredFlags(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	kv := &fakeKV{scopes: map[string]map[string]string{
		repository.ScopeCustomModels: {
			"cc|claude-custom|llm": `{"providerAlias":"cc","id":"claude-custom","type":"llm","caps":{"vision":true,"reasoning":false}}`,
			"broken":               `{`,
			"no-owner":             `{"id":"x","caps":{"vision":true}}`,
		},
	}}
	if err := LoadDeclared(context.Background(), kv, caps); err != nil {
		t.Fatal(err)
	}
	got := caps.For("claude", "claude-custom")
	if !got.Vision || got.Reasoning {
		t.Fatalf("%+v", got)
	}
	if err := LoadDeclared(context.Background(), &fakeKV{err: errors.New("down")}, caps); err == nil {
		t.Fatal("a failed read was reported as a load")
	}
}
