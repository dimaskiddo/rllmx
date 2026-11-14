package upstream

import (
	"testing"
	"testing/fstest"
)

func loadMedia(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	fs := fstest.MapFS{}
	for name, body := range files {
		fs["data/"+name+".json"] = &fstest.MapFile{Data: []byte(body)}
	}
	reg, err := Load(fs, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func TestAMediaModelResolvesBareAndPrefixed(t *testing.T) {
	reg := loadMedia(t, map[string]string{"jina-ai": `{
		"id":"jina-ai","category":"apikey","display":{"name":"Jina"},
		"media":{"serviceKinds":["embedding"],"embeddingConfig":{"baseUrl":"https://api.jina.ai/v1/embeddings","authType":"apikey","authHeader":"bearer",
		"models":[{"id":"jina-embeddings-v3","name":"v3","dimensions":1024}]}}}`})
	ref, ok := reg.Media("jina-embeddings-v3", KindEmbedding)
	if !ok || ref.Provider != "jina-ai" || ref.Kind != KindEmbedding {
		t.Fatalf("%+v %v", ref, ok)
	}
	if ref, ok := reg.Media("jina-ai/jina-embeddings-v3", KindEmbedding); !ok || ref.Model != "jina-embeddings-v3" {
		t.Fatalf("%+v %v", ref, ok)
	}
}

func TestAMediaIdTwoProvidersClaimIsAmbiguousBare(t *testing.T) {
	entry := func(id string) string {
		return `{"id":"` + id + `","category":"apikey","display":{"name":"P"},
		"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"https://x.example/i","models":[{"id":"shared"}]}}}`
	}
	reg := loadMedia(t, map[string]string{"a": entry("a"), "b": entry("b")})
	if _, ok := reg.Media("shared", KindImage); ok {
		t.Fatal("an ambiguous id resolved without a provider")
	}
	if ref, ok := reg.Media("a/shared", KindImage); !ok || ref.Provider != "a" {
		t.Fatalf("%+v %v", ref, ok)
	}
}

func TestMediaAcceptsTheReferenceShapes(t *testing.T) {
	for name, body := range map[string]string{
		"a hidden kind need not be served": `{"id":"p","category":"apikey","display":{"name":"P"},
			"media":{"serviceKinds":["image"],"hiddenKinds":["tts"],"imageConfig":{"baseUrl":"https://x.example/i"}}}`,
		"a sentinel address with no auth": `{"id":"p","category":"freeTier","display":{"name":"P"},
			"media":{"serviceKinds":["tts"],"noAuth":true,"ttsConfig":{"baseUrl":"edge-tts","authType":"none","authHeader":"none","models":[]}}}`,
		"a regional template": `{"id":"p","category":"apikey","display":{"name":"P"},
			"media":{"serviceKinds":["tts"],"ttsConfig":{"baseUrl":"https://polly.{region}.amazonaws.com/v1/speech","authType":"apikey","authHeader":"bearer"}}}`,
		"no served kind":                     `{"id":"p","category":"webCookie","display":{"name":"P"},"media":{"serviceKinds":[]}}`,
		"a declared kind without its config": `{"id":"p","category":"apikey","display":{"name":"P"},"media":{"serviceKinds":["embedding"]}}`,
		"a model map with a task": `{"id":"p","category":"apikey","display":{"name":"P"},
			"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"https://x.example/i","modelMap":{"hub/id":{"path":"fal/id","task":"image-to-image"}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			loadMedia(t, map[string]string{"p": body})
		})
	}
}

func TestMediaRefusesWhatItCannotServe(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown kind": `{"id":"p","category":"apikey","display":{"name":"P"},"media":{"serviceKinds":["sound"]}}`,

		"a sentinel address with a key": `{"id":"p","category":"apikey","display":{"name":"P"},
			"media":{"serviceKinds":["tts"],"ttsConfig":{"baseUrl":"edge-tts","authType":"apikey","authHeader":"bearer"}}}`,
		"a plain word with an auth type": `{"id":"p","category":"apikey","display":{"name":"P"},
			"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"not a url"}}}`,
		"a media id used as chat": `{"id":"p","category":"apikey","display":{"name":"P"},"models":[{"id":"m"}],
			"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"https://x.example/i","models":[{"id":"m"}]}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			fs := fstest.MapFS{"data/p.json": &fstest.MapFile{Data: []byte(body)}}
			if _, err := Load(fs, "data"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestAModelIdThatHoldsASlashResolvesBare(t *testing.T) {
	reg := loadMedia(t, map[string]string{"nvidia": `{"id":"nvidia","category":"freeTier","display":{"name":"NVIDIA"},
		"media":{"serviceKinds":["embedding"],"embeddingConfig":{"baseUrl":"https://integrate.api.nvidia.com/v1/embeddings",
		"models":[{"id":"nvidia/nv-embedqa-e5-v5"}]}}}`})
	if ref, ok := reg.Media("nvidia/nv-embedqa-e5-v5", KindEmbedding); !ok || ref.Provider != "nvidia" || ref.Model != "nvidia/nv-embedqa-e5-v5" {
		t.Fatalf("%+v %v", ref, ok)
	}
}
