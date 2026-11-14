package github

import (
	"context"
	"testing"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func TestTheCopilotIdentityHeadersGoWithTheCall(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := reg.ByID("github")
	if !ok || entry.Media == nil || entry.Media.Embedding == nil {
		t.Fatal("github serves no embeddings in its data")
	}
	call, err := Embeddings{}.Build(context.Background(), media.Request{
		Kind: upstream.KindEmbedding, Provider: "github", Entry: entry, Config: entry.Media.Embedding,
		Credential: "copilot-token", WireModel: "text-embedding-3-small", Body: []byte(`{"input":"hi"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.URL != "https://api.githubcopilot.com/embeddings" {
		t.Fatalf("url %s", call.URL)
	}
	if got := call.Header["Authorization"]; got != "Bearer copilot-token" {
		t.Fatalf("authorization %q", got)
	}
	for _, name := range []string{"copilot-integration-id", "editor-version", "user-agent", "x-github-api-version"} {
		if call.Header[name] == "" {
			t.Errorf("the %s header is missing", name)
		}
	}
}
