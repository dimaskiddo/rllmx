package video

import (
	"context"
	"net/http"
	"testing"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func openrouterRequest() media.Request {
	return media.Request{
		Kind: upstream.KindVideo, Provider: "openrouter", Credential: "KEY",
		Action: media.VideoGenerations, ContentType: "application/json",
		Body: []byte(`{"model":"google/veo-3.1","prompt":"a cat"}`),
		Config: &upstream.MediaConfig{
			BaseURL: "https://openrouter.ai/api/v1/videos", AuthType: "apikey", AuthHeader: "bearer",
			Headers: map[string]string{"HTTP-Referer": "https://example.test", "X-Title": "R"},
		},
	}
}

func TestOpenRouterCreatesAtTheCollectionRoot(t *testing.T) {
	call, err := (OpenRouter{}).Build(context.Background(), openrouterRequest(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.Method != http.MethodPost || call.URL != "https://openrouter.ai/api/v1/videos" {
		t.Fatalf("%s %s", call.Method, call.URL)
	}
	if call.Header["Authorization"] != "Bearer KEY" || call.Header["X-Title"] != "R" || call.Header["HTTP-Referer"] != "https://example.test" {
		t.Fatalf("%v", call.Header)
	}
}

func TestOpenRouterServesGenerationsOnly(t *testing.T) {
	r := openrouterRequest()
	r.Action = media.VideoEdits
	if _, err := (OpenRouter{}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("edits was accepted")
	}
}

func TestOpenRouterRefusesANonJSONBody(t *testing.T) {
	r := openrouterRequest()
	r.ContentType = "multipart/form-data; boundary=abc"
	if _, err := (OpenRouter{}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("a multipart body was accepted")
	}
}

func TestOpenRouterPollsByID(t *testing.T) {
	r := openrouterRequest()
	r.JobID = "job-1"
	call, err := (OpenRouter{}).Build(context.Background(), r, nil)
	if err != nil || call.Method != http.MethodGet || call.URL != "https://openrouter.ai/api/v1/videos/job-1" {
		t.Fatalf("%v %s", err, call.URL)
	}
}
