// Package mux is the one table of media adapters: which adapter serves a
// provider for a service. It is the only place that imports the adapter
// packages, the same way executor/mux builds the executor table.
package mux

import (
	"rllmx/app/llm/media"
	"rllmx/app/llm/media/antigravity"
	"rllmx/app/llm/media/codex"
	"rllmx/app/llm/media/github"
	"rllmx/app/llm/media/google"
	"rllmx/app/llm/media/imaging"
	"rllmx/app/llm/media/ollama"
	"rllmx/app/llm/media/openai"
	"rllmx/app/llm/media/selfhosted"
	"rllmx/app/llm/media/stt"
	"rllmx/app/llm/media/tts"
	"rllmx/app/llm/upstream"
)

// Table is the adapter table. It is read-only after New.
type Table struct {
	adapters map[string]map[string]media.Adapter
}

// New builds the table by explicit construction.
func New() *Table {
	t := &Table{adapters: map[string]map[string]media.Adapter{}}

	embeddings := openai.Embeddings{}
	for _, id := range []string{
		"openai", "openrouter", "mistral", "voyage-ai", "fireworks", "together",
		"nebius", "nvidia", "jina-ai", "vercel-ai-gateway",
	} {
		t.add(embeddings, id)
	}
	t.add(google.Embeddings{}, "gemini")
	t.add(github.Embeddings{}, "github")
	t.add(ollama.Embeddings{}, "ollama-local")
	t.add(selfhosted.Embeddings{}, "selfhosted-embedding")

	images := openai.Images{}
	for _, id := range []string{
		"openai", "minimax", "openrouter", "recraft", "vercel-ai-gateway", "xai", "sensenova",
	} {
		t.add(images, id)
	}
	t.add(google.Images{}, "gemini")
	for _, a := range []struct {
		adapter  media.Adapter
		provider string
	}{
		{imaging.Stability{}, "stability-ai"},
		{imaging.BlackForestLabs{}, "black-forest-labs"},
		{imaging.Fal{}, "fal-ai"},
		{imaging.HuggingFace{}, "huggingface"},
		{imaging.ComfyUI{}, "comfyui"},
		{imaging.SDWebUI{}, "sdwebui"},
		{imaging.Runway{}, "runwayml"},
		{imaging.Cloudflare{}, "cloudflare-ai"},
		{imaging.NanoBanana{}, "nanobanana"},
		{codex.Images{}, "codex"},
		{antigravity.Images{}, "antigravity"},
	} {
		t.add(a.adapter, a.provider)
	}

	for _, a := range []struct {
		adapter  media.Adapter
		provider string
	}{
		{tts.OpenAI{}, "openai"},
		{tts.LocalOpenAI{}, "selfhosted-tts"},
		{tts.LocalOpenAI{}, "ollama-local"},
		{tts.ElevenLabs{}, "elevenlabs"},
		{tts.Gemini{}, "gemini"},
		{tts.MiniMax{}, "minimax"},
		{tts.MiniMax{}, "minimax-cn"},
		{tts.Xiaomi{}, "xiaomi-mimo"},
		{tts.OpenRouter{}, "openrouter"},
		{tts.Cartesia{}, "cartesia"},
		{tts.Inworld{}, "inworld"},
		{tts.PlayHT{}, "playht"},
		{tts.FishAudio{}, "fish-audio"},
		{tts.NVIDIA{}, "nvidia"},
		{tts.Coqui{}, "coqui"},
		{tts.Tortoise{}, "tortoise"},
		{stt.OpenAI{}, "openai"},
		{stt.OpenAI{}, "groq"},
		{stt.OpenAI{}, "selfhosted-stt"},
		{stt.Deepgram{}, "deepgram"},
		{stt.AssemblyAI{}, "assemblyai"},
		{stt.ElevenLabs{}, "elevenlabs"},
		{stt.HuggingFace{}, "huggingface"},
		{stt.Gemini{}, "gemini"},
		{stt.NewCodex(), "codex"},
	} {
		t.add(a.adapter, a.provider)
	}
	// A model marker picks a second adapter for the same provider.
	t.adapters["gemini"]["stt|gemini-live"] = stt.GeminiLive{}
	return t
}

// add registers one adapter for providers of the kind it serves.
func (t *Table) add(a media.Adapter, providers ...string) {
	for _, id := range providers {
		if t.adapters[id] == nil {
			t.adapters[id] = map[string]media.Adapter{}
		}
		t.adapters[id][a.Kind()] = a
	}
}

// For returns the adapter of one provider and service. A transport marker may
// pick a second adapter (gemini-live); a user node speaks the OpenAI body for
// both node kinds that serve embeddings.
func (t *Table) For(provider, kind, transport string) (media.Adapter, bool) {
	if transport != "" {
		if a, ok := t.adapters[provider][kind+"|"+transport]; ok {
			return a, true
		}
	}
	if a, ok := t.adapters[provider][kind]; ok {
		return a, true
	}
	if kind == upstream.KindEmbedding {
		if k := upstream.NodeKind(provider); k == upstream.NodeTypeEmbedding || k == upstream.NodeTypeOpenAI {
			return openai.Embeddings{}, true
		}
	}
	return nil, false
}

// Adapter is the table as the gateway sees it.
var _ interface {
	For(provider, kind, transport string) (media.Adapter, bool)
} = (*Table)(nil)
