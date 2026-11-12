package video

import (
	"context"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func cfg() *upstream.MediaConfig {
	return &upstream.MediaConfig{BaseURL: "https://api.x.ai/v1/videos", AuthType: "apikey", AuthHeader: "bearer"}
}

func create() media.Request {
	return media.Request{
		Kind: upstream.KindVideo, Provider: "xai", Config: cfg(), Credential: "KEY",
		Action: media.VideoGenerations, ContentType: "application/json", IdempotencyKey: "idem-1",
		Body: []byte(`{"model":"grok-imagine-video","prompt":"a cat"}`),
	}
}

func TestXAICreateForwardsTheBodyAndTheKey(t *testing.T) {
	call, err := (XAI{}).Build(context.Background(), create(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.Method != http.MethodPost || call.URL != "https://api.x.ai/v1/videos/generations" {
		t.Fatalf("%s %s", call.Method, call.URL)
	}
	if call.Header["Authorization"] != "Bearer KEY" || call.Header["Idempotency-Key"] != "idem-1" ||
		call.Header["Content-Type"] != "application/json" {
		t.Fatalf("%v", call.Header)
	}
	if string(call.Body) != `{"model":"grok-imagine-video","prompt":"a cat"}` {
		t.Fatalf("%s", call.Body)
	}
}

func TestXAIPollIsAGetWithoutABody(t *testing.T) {
	r := create()
	r.JobID, r.Action, r.IdempotencyKey = "req-9", "", "idem-1"
	call, err := (XAI{}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.Method != http.MethodGet || call.URL != "https://api.x.ai/v1/videos/req-9" {
		t.Fatalf("%s %s", call.Method, call.URL)
	}
	if call.Body != nil || call.Header["Idempotency-Key"] != "" || call.Header["Content-Type"] != "" {
		t.Fatalf("%v %s", call.Header, call.Body)
	}
}

func TestAJOBIDWithASlashIsRefused(t *testing.T) {
	r := create()
	r.JobID = "projects/p/operations/o"
	if _, err := (XAI{}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("a job id carrying a slash was accepted")
	}
	r.JobID = "a b"
	if _, err := (XAI{}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("a job id carrying a space was accepted")
	}
}

func TestXAIPassesTheAnswerThrough(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	out, err := (XAI{}).Parse(context.Background(), create(), nil, 200, h, []byte(`{"request_id":"r1","status":"pending"}`))
	if err != nil || gjson.GetBytes(out.Body, "request_id").String() != "r1" || out.Header["Content-Type"] != "application/json" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestAMultipartBodyIsForwardedAsItArrived(t *testing.T) {
	r := create()
	r.ContentType = "multipart/form-data; boundary=abc"
	r.Body = []byte("--abc\r\ncontent\r\n--abc--")
	call, err := (XAI{}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(call.Body) != string(r.Body) || call.Header["Content-Type"] != r.ContentType {
		t.Fatalf("%s %v", call.Body, call.Header)
	}
}

func TestAnUnknownActionIsRefused(t *testing.T) {
	r := create()
	r.Action = "renders"
	if _, err := (XAI{}).Build(context.Background(), r, nil); err == nil {
		t.Fatal("an unknown action was accepted")
	}
}
