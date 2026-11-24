package antigravity

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

func antigravityEntry() upstream.Entry {
	return upstream.Entry{
		ID:        "antigravity",
		Transport: upstream.Transport{BaseURL: "https://daily-cloudcode-pa.googleapis.com/v1internal", Headers: map[string]string{"User-Agent": "antigravity/hub/2.33.0 darwin/arm64"}},
	}
}

func TestTheBodyCarriesTheProjectTheRatioAndTheParts(t *testing.T) {
	call, err := (Images{}).Build(context.Background(), media.Request{
		Kind: upstream.KindImage, WireModel: "gemini-3.1-flash-image", Entry: antigravityEntry(),
		Credential: "tok", Account: map[string]string{"projectId": "proj-1"},
		Body: []byte(`{"prompt":"a cat","size":"1024x1792"}`),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if call.URL != "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent" {
		t.Fatalf("%s", call.URL)
	}
	body := gjson.ParseBytes(call.Body)
	if body.Get("project").String() != "proj-1" || body.Get("model").String() != "gemini-3.1-flash-image" ||
		body.Get("request.generationConfig.imageConfig.aspectRatio").String() != "9:16" {
		t.Fatalf("%s", call.Body)
	}
	if got := body.Get("request.contents.0.parts.0.text").String(); got != "a cat" {
		t.Fatalf("prompt %q", got)
	}
	if body.Get("requestId").String() == "" || body.Get("request.sessionId").String() == "" {
		t.Fatalf("no ids: %s", call.Body)
	}
}

func TestAModelSuffixChoosesTheRatio(t *testing.T) {
	model, ratio := modelAndRatio("gemini-3.1-flash-image-16x9", "", "")
	if model != "gemini-3.1-flash-image" || ratio != "16:9" {
		t.Fatalf("%s %s", model, ratio)
	}
	if _, ratio := modelAndRatio("m", "1024x1024", "4:3"); ratio != "4:3" {
		t.Fatalf("ratio %s", ratio)
	}
}

func TestAnAccountWithoutAProjectIsRefused(t *testing.T) {
	_, err := (Images{}).Build(context.Background(), media.Request{
		Kind: upstream.KindImage, WireModel: "m", Entry: antigravityEntry(), Credential: "tok",
		Body: []byte(`{"prompt":"x"}`),
	}, nil)
	if err == nil {
		t.Fatal("accepted")
	}
}

func TestAnAnswerWithoutAnImageIsAnError(t *testing.T) {
	if _, err := (Images{}).Parse(context.Background(), media.Request{}, nil, 200, nil, []byte(`{"candidates":[{"content":{"parts":[{"text":"no"}]}}]}`)); err == nil {
		t.Fatal("accepted")
	}
	out, err := (Images{}).Parse(context.Background(), media.Request{}, nil, 200, nil,
		[]byte(`{"candidates":[{"content":{"parts":[{"inlineData":{"data":"QUJD"}}]}}]}`))
	if err != nil || gjson.GetBytes(out.Body, "data.0.b64_json").String() != "QUJD" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

var _ = media.Response{}
