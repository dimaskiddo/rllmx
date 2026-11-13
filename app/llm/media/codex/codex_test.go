package codex

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func jwt(claims string) string {
	return "h." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".s"
}

func TestOnlyAPlainPromptOnAToolModelIsServed(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://chatgpt.com/backend-api/codex/images/generations"}
	for name, body := range map[string]string{
		"a size":             `{"prompt":"x","size":"1024x1024"}`,
		"two images":         `{"prompt":"x","n":2}`,
		"a bad quality":      `{"prompt":"x","quality":"ultra"}`,
		"a jpeg":             `{"prompt":"x","output_format":"jpeg"}`,
		"output compression": `{"prompt":"x","output_compression":5}`,
	} {
		_, err := (Images{}).Build(context.Background(), media.Request{
			Kind: upstream.KindImage, WireModel: "gpt-image-2", Config: cfg, Credential: "tok", Body: []byte(body),
		}, nil)
		var br *media.BadRequest
		if !errors.As(err, &br) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := (Images{}).Build(context.Background(), media.Request{
		Kind: upstream.KindImage, WireModel: "gpt-4o", Config: cfg, Credential: "tok", Body: []byte(`{"prompt":"x"}`),
	}, nil); err == nil {
		t.Fatal("a chat model was accepted")
	}
}

func TestTheBodyIsTheNativeShape(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://chatgpt.com/backend-api/codex/images/generations"}
	call, err := (Images{}).Build(context.Background(), media.Request{
		Kind: upstream.KindImage, WireModel: "gpt-image-2", Config: cfg, Credential: "tok",
		Body: []byte(`{"prompt":"a cat","quality":"high"}`), Account: map[string]string{"chatgptAccountId": "acct-1"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := gjson.ParseBytes(call.Body)
	if body.Get("model").String() != "gpt-image-2" || body.Get("size").String() != "auto" ||
		body.Get("quality").String() != "high" || body.Get("n").Int() != 1 || body.Get("output_format").String() != "png" {
		t.Fatalf("%s", call.Body)
	}
	if call.Header["chatgpt-account-id"] != "acct-1" || call.Header["session_id"] == "" || call.Header["x-client-request-id"] == "" {
		t.Fatalf("%v", call.Header)
	}
}

func TestAFreePlanIsRefusedBeforeAnyCall(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://chatgpt.com/backend-api/codex/images/generations"}
	_, err := (Images{}).Build(context.Background(), media.Request{
		Kind: upstream.KindImage, WireModel: "gpt-image-2", Config: cfg,
		Credential: jwt(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"free"}}`),
		Body:       []byte(`{"prompt":"a cat"}`),
	}, nil)
	var ne *media.NotEntitled
	if !errors.As(err, &ne) {
		t.Fatalf("%v", err)
	}
}

func TestTheAnswerIsNormalized(t *testing.T) {
	out, err := (Images{}).Parse(context.Background(), media.Request{}, nil, 200, nil,
		[]byte(`{"created":7,"data":[{"b64_json":"QUJD"}],"usage":{"input_tokens":3,"output_tokens":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out.Body, "created").Int() != 7 || gjson.GetBytes(out.Body, "data.0.b64_json").String() != "QUJD" {
		t.Fatalf("%s", out.Body)
	}
	if !out.Usage.Known || out.Usage.Prompt != 3 || out.Usage.Completion != 4 {
		t.Fatalf("%+v", out.Usage)
	}
	if _, err := (Images{}).Parse(context.Background(), media.Request{}, nil, 200, nil, []byte(`{"data":[]}`)); err == nil {
		t.Fatal("an answer without an image was accepted")
	}
}

func TestAnEditGoesToTheNativeEditsEndpoint(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://chatgpt.com/backend-api/codex/images/generations"}
	call, err := (Images{}).Build(context.Background(), media.Request{
		Kind: upstream.KindImage, WireModel: "gpt-image-2", Config: cfg, Credential: "tok",
		Body: []byte(`{"prompt":"make it blue","images":["aGk=","https://x/i.png","data:image/jpeg;base64,QQ=="],"image":"aGk=","quality":"high"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.URL != "https://chatgpt.com/backend-api/codex/images/edits" {
		t.Fatalf("%s", call.URL)
	}
	body := gjson.ParseBytes(call.Body)
	if body.Get("model").String() != "gpt-image-2" || body.Get("prompt").String() != "make it blue" ||
		body.Get("n").Int() != 1 || body.Get("size").String() != "auto" || body.Get("quality").String() != "high" ||
		body.Get("background").String() != "auto" || body.Get("output_format").Exists() {
		t.Fatalf("%s", call.Body)
	}
	images := body.Get("images").Array()
	if len(images) != 4 {
		t.Fatalf("%d images: %s", len(images), call.Body)
	}
	for i, want := range []string{"data:image/png;base64,aGk=", "https://x/i.png", "data:image/jpeg;base64,QQ==", "data:image/png;base64,aGk="} {
		if got := images[i].Get("image_url").String(); got != want {
			t.Errorf("image %d: %q, want %q", i, got, want)
		}
	}
	// An edit with an output control still gets the 400 the native endpoint cannot honour.
	for name, body := range map[string]string{
		"a size":    `{"prompt":"x","image":"aGk=","size":"1024x1024"}`,
		"a jpeg":    `{"prompt":"x","image":"aGk=","output_format":"jpeg"}`,
		"two shots": `{"prompt":"x","image":"aGk=","n":2}`,
	} {
		if _, err := (Images{}).Build(context.Background(), media.Request{
			Kind: upstream.KindImage, WireModel: "gpt-image-2", Config: cfg, Credential: "tok", Body: []byte(body),
		}, nil); err == nil {
			t.Errorf("%s: an edit with an output control was accepted", name)
		}
	}
}
