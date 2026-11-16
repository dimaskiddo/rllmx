package media_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/access/identity"
	gatewaymedia "rllmx/app/gateway/media"
)

// restricted is a key limited to one entry.
func restricted(allow ...string) identity.Caller {
	return identity.Caller{ID: "k", Restricted: true, Allow: allow}
}

func TestAMediaModelIsCheckedAgainstTheList(t *testing.T) {
	cases := []struct {
		name string
		call gatewaymedia.Input
		run  func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output
	}{
		{
			name: "an embedding",
			call: gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"model":"text-embedding-3-small","input":"hi"}`)},
			run: func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
				return p.Embeddings(context.Background(), in)
			},
		},
		{
			name: "an image",
			call: gatewaymedia.Input{Path: "/v1/images/generations", Body: []byte(`{"model":"gpt-image-1","prompt":"p"}`)},
			run: func(p *gatewaymedia.Pipeline, in gatewaymedia.Input) gatewaymedia.Output {
				return p.Images(context.Background(), in)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, mediaEntry)
			listed := tc.call
			listed.Caller = restricted("openai/text-embedding-3-small", "openai/gpt-image-1")
			if out := tc.run(r.p, listed); out.Status != http.StatusOK {
				t.Fatalf("a listed model: %d %s", out.Status, out.Body)
			}
			unlisted := tc.call
			unlisted.Caller = restricted("something/else")
			out := tc.run(r.p, unlisted)
			if out.Status != http.StatusForbidden || !strings.Contains(string(out.Body), "not allowed") {
				t.Fatalf("an unlisted model: %d %s", out.Status, out.Body)
			}
		})
	}
}

func TestASpeechNamesTheVoiceTheClientGave(t *testing.T) {
	p, _ := audioRig(t, allowAudio, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte(strings.Repeat("a", 200)))
	})
	in := gatewaymedia.Input{Path: "/v1/audio/speech", Body: []byte(`{"model":"openai/alloy","input":"hi"}`)}
	in.Caller = restricted("openai/alloy")
	if out := p.Speech(context.Background(), in); out.Status != http.StatusOK {
		t.Fatalf("a listed voice: %d %s", out.Status, out.Body)
	}
	in.Caller = restricted("openai/tts-1")
	if out := p.Speech(context.Background(), in); out.Status != http.StatusForbidden {
		t.Fatalf("the model id allowed the voice: %d %s", out.Status, out.Body)
	}
}

func TestASearchNamesItsProvider(t *testing.T) {
	f := &fakeServer{body: `{"organic":[{"title":"A","link":"https://example.com/a"}]}`}
	r := newServiceRig(t, f, nil, map[string]string{"serper": serperEntry}, serperKey())
	in := gatewaymedia.Input{Path: "/v1/search", Body: []byte(`{"provider":"serper","query":"hello"}`)}
	in.Caller = restricted("serper")
	if out := r.p.Search(context.Background(), in); out.Status != http.StatusOK {
		t.Fatalf("a listed provider: %d %s", out.Status, out.Body)
	}
	unlisted := in
	unlisted.Caller = restricted("tavily")
	calls := len(f.calls)
	if out := r.p.Search(context.Background(), unlisted); out.Status != http.StatusForbidden {
		t.Fatalf("an unlisted provider: %d %s", out.Status, out.Body)
	}
	if len(f.calls) != calls {
		t.Fatal("a refused search reached the provider")
	}
}

func TestAVideoPollNeedsTheProviderItNames(t *testing.T) {
	p, _, _ := videoRig(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"request_id":"r1","status":"completed"}`))
	}, "KEY")
	in := gatewaymedia.Input{Path: "/v1/videos/r1", Caller: restricted("xai")}
	if out := p.VideoJob(context.Background(), in, "r1"); out.Status != http.StatusOK {
		t.Fatalf("a listed provider: %d %s", out.Status, out.Body)
	}
	unlisted := in
	unlisted.Caller = restricted("openai")
	if out := p.VideoJob(context.Background(), unlisted, "r1"); out.Status != http.StatusForbidden {
		t.Fatalf("an unlisted provider: %d %s", out.Status, out.Body)
	}
}

// allowAudio is one speech provider with one voice.
const allowAudio = `{"id":"openai","category":"apikey","display":{"name":"OpenAI"},
"media":{"serviceKinds":["tts"],
 "ttsConfig":{"baseUrl":"@/v1/audio/speech","authType":"apikey","authHeader":"bearer","defaultModel":"tts-1","models":[{"id":"tts-1","name":"TTS-1"}],"voices":[{"id":"alloy","name":"Alloy","lang":"en-US"}]}},
"models":[{"id":"tts-1","kind":"tts"}]}`

func TestTheVoiceListIsCheckedByProvider(t *testing.T) {
	p, _ := audioRig(t, allowAudio, func(w http.ResponseWriter, _ *http.Request) {})
	if out := p.Voices(context.Background(), identity.Caller{}, "openai", ""); out.Status != http.StatusOK {
		t.Fatalf("an open key: %d", out.Status)
	}
	if out := p.Voices(context.Background(), restricted("openai"), "openai", ""); out.Status != http.StatusOK {
		t.Fatalf("a listed provider: %d %s", out.Status, out.Body)
	}
	if out := p.Voices(context.Background(), restricted("elevenlabs"), "openai", ""); out.Status != http.StatusForbidden {
		t.Fatalf("an unlisted provider: %d %s", out.Status, out.Body)
	}
}
