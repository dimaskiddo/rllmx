package imaging

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// HuggingFace speaks the router's image path: <base>/<mapped path>, binary back.
type HuggingFace struct{}

// Kind is the service it serves.
func (HuggingFace) Kind() string { return upstream.KindImage }

// Build resolves the router path and the body for the model's task.
func (HuggingFace) Build(ctx context.Context, r media.Request, c media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	target := ""
	task := "text-to-image"
	if custom := strings.TrimRight(r.Account["baseUrl"], "/"); custom != "" {
		if strings.Contains(r.WireModel, "..") || strings.Contains(r.WireModel, "//") ||
			strings.ContainsAny(r.WireModel, "?#") {
			return media.Call{}, media.BadRequestf("the model names an address")
		}
		target = custom + "/" + r.WireModel
	} else {
		if base == "" || r.Config == nil {
			return media.Call{}, media.BadRequestf("the provider names no address")
		}
		t, ok := r.Config.ModelMap[r.WireModel]
		if !ok || t.Path == "" {
			return media.Call{}, media.BadRequestf("the provider does not know that model")
		}
		target = base + "/" + t.Path
		if t.Task != "" {
			task = t.Task
		}
	}
	body := gjson.ParseBytes(r.Body)
	var payload []byte
	if task == "image-to-image" {
		raw := body.Get("image").String()
		if raw == "" {
			raw = body.Get("images.0").String()
		}
		if strings.TrimSpace(raw) == "" {
			return media.Call{}, media.BadRequestf("the model needs a source image")
		}
		source, err := media.InlineImage(ctx, c, raw)
		if err != nil {
			return media.Call{}, err
		}
		payload, err = json.Marshal(map[string]any{"inputs": source, "parameters": map[string]string{"prompt": body.Get("prompt").String()}})
		if err != nil {
			return media.Call{}, media.BadRequestf("the request cannot be built")
		}
	} else {
		raw, err := json.Marshal(map[string]string{"inputs": body.Get("prompt").String()})
		if err != nil {
			return media.Call{}, media.BadRequestf("the request cannot be built")
		}
		payload = raw
	}
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: target, Header: h, Body: payload}, nil
}

// Parse reads raw image bytes.
func (HuggingFace) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	if b64, _ := media.BinaryImage(body); b64 != "" {
		return imageList([]string{b64}), nil
	}
	return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
}
