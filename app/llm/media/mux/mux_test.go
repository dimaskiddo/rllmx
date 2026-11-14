package mux

import (
	"testing"

	"rllmx/app/llm/upstream"
)

// TestEveryServedMediaKindHasAHandler walks the shipped data: a provider that
// lists a service in its own data must have a handler for it, or its models
// resolve and then answer "does not serve that model here".
func TestEveryServedMediaKindHasAHandler(t *testing.T) {
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	table := New(Options{})
	// Providers whose service is recorded as not built, with the reason in the
	// design docs: a scraped speech engine, a spawned process, a browser or a
	// TLS-fingerprint client.
	skipped := map[string]bool{
		"edge-tts": true, "google-tts": true, "local-device": true, "aws-polly": true,
		"grok-web": true, "gemini-web": true, "muse-web": true,
	}
	for _, entry := range reg.All() {
		id := entry.ID
		if entry.Media == nil || skipped[id] {
			continue
		}
		for _, kind := range []string{
			upstream.KindEmbedding, upstream.KindImage, upstream.KindTTS, upstream.KindSTT,
			upstream.KindVideo, upstream.KindSearch, upstream.KindFetch,
		} {
			if entry.Media.Config(kind) == nil && !(kind == upstream.KindSearch && entry.Media.SearchViaChat != nil) {
				continue
			}
			if _, ok := table.For(id, kind, ""); !ok {
				t.Errorf("%s lists %s in its data and has no handler for it", id, kind)
			}
		}
	}
}
