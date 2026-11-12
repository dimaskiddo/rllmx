package imaging

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// fake answers media calls from a script and records them.
type fake struct {
	calls []string
	do    func(method, target string) (int, http.Header, []byte, error)
}

func (f *fake) Do(_ context.Context, method, target string, _ map[string]string, body []byte) (int, http.Header, []byte, error) {
	f.calls = append(f.calls, method+" "+target+" "+string(body))
	return f.do(method, target)
}

func png() []byte {
	return []byte("\x89PNG\r\n\x1a\nDATA")
}

func req(kind, provider, model string, cfg *upstream.MediaConfig, body string) media.Request {
	return media.Request{Kind: kind, Provider: provider, Model: model, WireModel: model, Config: cfg, Credential: "KEY",
		Body: []byte(body), Account: map[string]string{"accountId": "acct"}}
}

func TestStabilitySendsMultipartAndReadsTheImage(t *testing.T) {
	f := &fake{do: func(string, string) (int, http.Header, []byte, error) {
		return 200, nil, []byte(`{"image":"QUJD"}`), nil
	}}
	cfg := &upstream.MediaConfig{BaseURL: "https://api.stability.ai/v2beta/stable-image/generate"}
	call, err := (Stability{}).Build(context.Background(), req(upstream.KindImage, "stability-ai", "sd3.5-large", cfg, `{"prompt":"a cat","size":"1024x1792"}`), f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(call.URL, "/sd3") {
		t.Fatalf("url %s", call.URL)
	}
	_, params, err := mime.ParseMediaType(call.Header["Content-Type"])
	if err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(strings.NewReader(string(call.Body)), params["boundary"]).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	if form.Value["prompt"][0] != "a cat" || form.Value["aspect_ratio"][0] != "9:16" || form.Value["output_format"][0] != "png" {
		t.Fatalf("%v", form.Value)
	}
	out, err := (Stability{}).Parse(context.Background(), media.Request{}, f, 200, nil, []byte(`{"image":"QUJD"}`))
	if err != nil || gjson.GetBytes(out.Body, "data.0.b64_json").String() != "QUJD" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestBlackForestLabsPollsItsOwnAddress(t *testing.T) {
	f := &fake{do: func(method, target string) (int, http.Header, []byte, error) {
		if method == http.MethodPost {
			return 200, nil, []byte(`{"polling_url":"https://api.bfl.ai/poll/1"}`), nil
		}
		return 200, nil, []byte(`{"status":"Ready","result":{"sample":"https://img/1.png"}}`), nil
	}}
	cfg := &upstream.MediaConfig{BaseURL: "https://api.bfl.ai/v1"}
	out, err := (BlackForestLabs{}).Parse(context.Background(), req(upstream.KindImage, "black-forest-labs", "flux-dev", cfg, ""), f, 200, nil, []byte(`{"polling_url":"https://api.bfl.ai/poll/1"}`))
	if err != nil || gjson.GetBytes(out.Body, "data.0.url").String() != "https://img/1.png" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestFalReadsTheQueueResult(t *testing.T) {
	f := &fake{do: func(_, target string) (int, http.Header, []byte, error) {
		switch {
		case strings.Contains(target, "/status/"):
			return 200, nil, []byte(`{"status":"COMPLETED"}`), nil
		case strings.Contains(target, "/result/"):
			return 200, nil, []byte(`{"images":[{"url":"https://img/2.png"}]}`), nil
		}
		return 404, nil, nil, nil
	}}
	body := []byte(`{"status_url":"https://queue.fal.run/status/1","response_url":"https://queue.fal.run/result/1"}`)
	out, err := (Fal{}).Parse(context.Background(), req(upstream.KindImage, "fal-ai", "fal-ai/flux/dev", &upstream.MediaConfig{BaseURL: "https://queue.fal.run"}, ""), f, 200, nil, body)
	if err != nil || gjson.GetBytes(out.Body, "data.0.url").String() != "https://img/2.png" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestHuggingFaceReadsBinaryAndMapsThePath(t *testing.T) {
	cfg := &upstream.MediaConfig{
		BaseURL:  "https://router.huggingface.co",
		ModelMap: map[string]upstream.ModelTarget{"hub/one": {Path: "fal/one"}},
	}
	call, err := (HuggingFace{}).Build(context.Background(), req(upstream.KindImage, "huggingface", "hub/one", cfg, `{"prompt":"hi"}`), nil)
	if err != nil || call.URL != "https://router.huggingface.co/fal/one" {
		t.Fatalf("%s %v", call.URL, err)
	}
	out, err := (HuggingFace{}).Parse(context.Background(), media.Request{}, nil, 200, nil, png())
	if err != nil {
		t.Fatal(err)
	}
	if b := gjson.GetBytes(out.Body, "data.0.b64_json").String(); b != base64.StdEncoding.EncodeToString(png()) {
		t.Fatalf("b64 %q", b)
	}
}

func TestSDWebUIReadsItsImages(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "http://localhost:7860"}
	call, err := (SDWebUI{}).Build(context.Background(), req(upstream.KindImage, "sdwebui", "sdxl", cfg, `{"prompt":"hi","size":"1024x1024","n":2}`), nil)
	if err != nil || !strings.HasSuffix(call.URL, "/sdapi/v1/txt2img") {
		t.Fatalf("%s %v", call.URL, err)
	}
	var sent map[string]any
	if err := json.Unmarshal(call.Body, &sent); err != nil || sent["batch_size"].(float64) != 2 {
		t.Fatalf("%s %v", call.Body, err)
	}
	out, err := (SDWebUI{}).Parse(context.Background(), media.Request{}, nil, 200, nil, []byte(`{"images":["QQ=="]}`))
	if err != nil || gjson.GetBytes(out.Body, "data.0.b64_json").String() != "QQ==" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestRunwayPollsTheTask(t *testing.T) {
	f := &fake{do: func(_, target string) (int, http.Header, []byte, error) {
		if strings.Contains(target, "/tasks/") {
			return 200, nil, []byte(`{"status":"SUCCEEDED","output":["https://img/3.png"]}`), nil
		}
		return 404, nil, nil, nil
	}}
	cfg := &upstream.MediaConfig{BaseURL: "https://api.dev.runwayml.com/v1"}
	call, err := (Runway{}).Build(context.Background(), req(upstream.KindImage, "runwayml", "gen4_image", cfg, `{"prompt":"hi","size":"1024x1792"}`), f)
	if err != nil || !strings.HasSuffix(call.URL, "/text_to_image") || !strings.Contains(string(call.Body), `"ratio":"1024:1792"`) {
		t.Fatalf("%s %s %v", call.URL, call.Body, err)
	}
	out, err := (Runway{}).Parse(context.Background(), req(upstream.KindImage, "runwayml", "gen4_image", cfg, ""), f, 200, nil, []byte(`{"id":"t1"}`))
	if err != nil || gjson.GetBytes(out.Body, "data.0.url").String() != "https://img/3.png" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestCloudflareRequiresTheAccountAndReadsBinary(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.cloudflare.com/client/v4/accounts"}
	if _, err := (Cloudflare{}).Build(context.Background(), media.Request{WireModel: "@cf/x", Config: cfg}, nil); err == nil {
		t.Fatal("a connection without an account was accepted")
	}
	call, err := (Cloudflare{}).Build(context.Background(), req(upstream.KindImage, "cloudflare-ai", "@cf/x", cfg, `{"prompt":"hi"}`), nil)
	if err != nil || !strings.Contains(call.URL, "/acct/ai/run/@cf/x") {
		t.Fatalf("%s %v", call.URL, err)
	}
	h := http.Header{}
	h.Set("Content-Type", "image/png")
	out, err := (Cloudflare{}).Parse(context.Background(), media.Request{}, nil, 200, h, png())
	if err != nil || gjson.GetBytes(out.Body, "data.0.b64_json").String() == "" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestNanoBananaPollsTheRecord(t *testing.T) {
	f := &fake{do: func(_, target string) (int, http.Header, []byte, error) {
		if strings.Contains(target, "record-info") {
			return 200, nil, []byte(`{"data":{"successFlag":1,"response":{"resultImageUrl":"https://img/4.png"}}}`), nil
		}
		return 404, nil, nil, nil
	}}
	cfg := &upstream.MediaConfig{BaseURL: "https://api.nanobananaapi.ai/api/v1/nanobanana/generate", PollURL: "https://api.nanobananaapi.ai/api/v1/nanobanana/record-info"}
	out, err := (NanoBanana{}).Parse(context.Background(), req(upstream.KindImage, "nanobanana", "nanobanana-pro", cfg, ""), f, 200, nil, []byte(`{"code":200,"data":{"taskId":"t9"}}`))
	if err != nil || gjson.GetBytes(out.Body, "data.0.url").String() != "https://img/4.png" {
		t.Fatalf("%s %v", out.Body, err)
	}
}

func TestRunwaySendsAVideoModelToImageToVideo(t *testing.T) {
	cfg := &upstream.MediaConfig{BaseURL: "https://api.dev.runwayml.com/v1"}
	r := media.Request{
		Kind: upstream.KindImage, Provider: "runwayml", Model: "gen4_turbo", WireModel: "gen4_turbo",
		Config: cfg, Credential: "KEY",
		Entry: upstream.Entry{Models: []upstream.Model{{ID: "gen4_turbo", Kind: upstream.KindVideo}}},
		Body:  []byte(`{"prompt":"hi","size":"1280x720","image":"https://src/1.png"}`),
	}
	call, err := (Runway{}).Build(context.Background(), r, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := gjson.ParseBytes(call.Body)
	if !strings.HasSuffix(call.URL, "/image_to_video") || body.Get("promptImage").String() != "https://src/1.png" ||
		body.Get("duration").Int() != 5 || body.Get("ratio").String() != "1280:720" {
		t.Fatalf("%s %s", call.URL, call.Body)
	}
}
