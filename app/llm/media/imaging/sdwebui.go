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

// SDWebUI speaks Automatic1111's txt2img.
type SDWebUI struct{}

// Kind is the service it serves.
func (SDWebUI) Kind() string { return upstream.KindImage }

// Build makes the txt2img call.
func (SDWebUI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	body := gjson.ParseBytes(r.Body)
	width, height := media.SizeToDimensions(body.Get("size").String())
	if width == 0 || height == 0 {
		width, height = 512, 512
	}
	n := 1
	if v, ok := intField(body, "n"); ok && v > 0 && v <= 10 {
		n = v
	}
	payload, err := json.Marshal(map[string]any{
		"prompt": body.Get("prompt").String(), "width": width, "height": height, "steps": 20, "batch_size": n,
	})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	return media.Call{Method: http.MethodPost, URL: base + "/sdapi/v1/txt2img", Header: map[string]string{"Content-Type": "application/json"}, Body: payload}, nil
}

// Parse reads the base64 images.
func (SDWebUI) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	var images []string
	for _, img := range gjson.GetBytes(body, "images").Array() {
		if img.Type == gjson.String && img.String() != "" {
			images = append(images, img.String())
		}
	}
	if len(images) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	_ = header
	return imageList(images), nil
}
