package pipeline

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/prefetch"
	"rllmx/tests"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\n0000")

const (
	geminiOK = `{"candidates":[{"content":{"parts":[{"text":"ok"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"totalTokenCount":4}}`
)

func imageBody(model, url string) string {
	return `{"model":"` + model + `","messages":[{"role":"user","content":[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"` + url + `"}}]}]}`
}

func imageServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		w.Write(pngBytes)
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

func TestAGeminiUpstreamGetsTheImageInline(t *testing.T) {
	img, hits := imageServer(t)
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiOK})
	r.p.d.Prefetch = &prefetch.Fetcher{Client: img.Client()}
	r.conn("gem", "gk")
	out, _ := r.do("/v1/chat/completions", imageBody("gem/g-vision", img.URL+"/pic"))
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	body := r.up.Calls()[0].Body
	if gjson.GetBytes(body, "contents.0.parts.1.inlineData.mimeType").String() != "image/png" || gjson.GetBytes(body, "contents.0.parts.1.inlineData.data").String() == "" {
		t.Fatalf("%s", body)
	}
	if hits.Load() != 1 {
		t.Fatalf("%d fetches", hits.Load())
	}
}

func TestOnlyGeminiAndOllamaTargetsNeedInlineImages(t *testing.T) {
	for format, want := range map[string]bool{"gemini": true, "ollama": true, "openai": false, "claude": false, "openai-responses": false} {
		if needsInlineImages(format) != want {
			t.Errorf("%s: want %v", format, want)
		}
	}
}

func TestAnOpenAITargetKeepsTheImageURL(t *testing.T) {
	img, hits := imageServer(t)
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Prefetch = &prefetch.Fetcher{Client: img.Client()}
	r.conn(nodeID, "k1")
	url := img.URL + "/pic"
	out, _ := r.do("/v1/chat/completions", imageBody(nodeID+"/gpt-vision", url))
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := gjson.GetBytes(r.up.Calls()[0].Body, "messages.0.content.1.image_url.url").String(); got != url || hits.Load() != 0 {
		t.Fatalf("url=%q fetches=%d: %s", got, hits.Load(), r.up.Calls()[0].Body)
	}
}

func TestAFailedFetchStillSendsTheRequest(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiOK})
	r.p.d.Prefetch = &prefetch.Fetcher{Client: &http.Client{Transport: failing{}}}
	r.conn("gem", "gk")
	out, _ := r.do("/v1/chat/completions", imageBody("gem/g-vision", "https://img.invalid/pic"))
	if out.Status != 200 || len(r.up.Calls()) != 1 {
		t.Fatalf("%d %s calls=%d", out.Status, out.Body, len(r.up.Calls()))
	}
}

type failing struct{}

func (failing) RoundTrip(*http.Request) (*http.Response, error) { return nil, http.ErrHandlerTimeout }
