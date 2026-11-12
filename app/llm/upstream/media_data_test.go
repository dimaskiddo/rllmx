package upstream

import "testing"

// The embedded set must carry every media provider the plan names, with each
// declared kind backed by a config or by an executor-driven body.
func TestTheShippedMediaDataLoadsAndIndexes(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range reg.All() {
		if e.Media == nil {
			continue
		}
		for _, kind := range e.Media.ServiceKinds {
			switch kind {
			case KindLLM, KindSystem, KindImageText:
				continue
			}
			cfg := e.Media.Config(kind)
			if cfg == nil {
				// An executor-driven web provider names no address.
				continue
			}
			if cfg.BaseURL == "" && cfg.AuthType != "none" && kind != KindSearch && !cfg.ExecutorDriven {
				t.Errorf("%s: %s has no address", e.ID, kind)
			}
		}
	}
	for _, want := range []struct{ name, kind string }{
		{"openai/text-embedding-3-large", KindEmbedding},
		{"voyage-3.5", KindEmbedding},
		{"openai/gpt-image-2.5", KindImage},
		{"sd3.5-large", KindImage},
		{"black-forest-labs/flux-dev", KindImage},
		{"grok-2-image-1212", KindImage},
		{"gemini-3.1-flash-image-preview", KindImage},
		{"@cf/black-forest-labs/flux-2-dev", KindImage},
		{"huggingface/black-forest-labs/FLUX.1-schnell", KindImage},
		{"nvidia/nv-embedqa-e5-v5", KindEmbedding},
		{"qwen3-embedding:8b", KindEmbedding},
	} {
		if ref, ok := reg.Media(want.name, want.kind); !ok || ref.Kind != want.kind {
			t.Errorf("%s (%s) is not indexed: %+v %v", want.name, want.kind, ref, ok)
		}
	}
}
