// Package antigravity serves the Antigravity image model through Cloud Code's
// own generateContent envelope, the body its desktop client sends.
package antigravity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Images speaks the image generation request of the Antigravity executor.
type Images struct{}

// Kind is the service it serves.
func (Images) Kind() string { return upstream.KindImage }

// Build makes the generateContent call.
func (Images) Build(ctx context.Context, r media.Request, c media.Client) (media.Call, error) {
	base := strings.TrimRight(r.Entry.Transport.BaseURL, "/")
	if r.Config != nil && r.Config.BaseURL != "" {
		base = strings.TrimRight(r.Config.BaseURL, "/")
	}
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	project := strings.TrimSpace(r.Account["projectId"])
	if project == "" || strings.ContainsAny(project, " \t\r\n\"\\") {
		return media.Call{}, media.BadRequestf("the account has no project")
	}
	body := gjson.ParseBytes(r.Body)
	model, ratio := modelAndRatio(r.WireModel, body.Get("size").String(), body.Get("aspect_ratio").String())
	prompt := body.Get("prompt").String()
	if neg := body.Get("negative_prompt").String(); neg != "" {
		prompt += "\n\nNegative prompt: " + neg
	}
	parts := []map[string]any{}
	var sources []string
	for _, name := range []string{"reference_images", "images"} {
		for _, v := range body.Get(name).Array() {
			if v.Type == gjson.String && v.String() != "" {
				sources = append(sources, v.String())
			}
		}
	}
	for _, name := range []string{"reference_image", "image"} {
		if v := body.Get(name).String(); v != "" {
			sources = append(sources, v)
		}
	}
	for i, source := range sources {
		if i >= 10 {
			break
		}
		b64, err := media.InlineImage(ctx, c, source)
		if err != nil || b64 == "" {
			// The reference drops a reference it cannot read; so do we.
			continue
		}
		parts = append(parts, map[string]any{"inlineData": map[string]string{"mimeType": "image/png", "data": b64}})
	}
	parts = append(parts, map[string]any{"text": prompt})
	reqBody := map[string]any{
		"project":     project,
		"model":       model,
		"userAgent":   "antigravity",
		"requestType": "image_gen",
		"requestId":   "agent/" + media.UUID() + "/" + strconv.FormatInt(time.Now().UnixMilli(), 10) + "/" + media.UUID() + "/1",
		"request": map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": parts}},
			"generationConfig": map[string]any{
				"temperature": 1.0, "topP": 0.95, "topK": 40, "maxOutputTokens": 8192,
				"imageConfig": map[string]string{"aspectRatio": ratio},
			},
			"sessionId": media.UUID(),
		},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := media.Headers(r, url.Values{})
	h["Content-Type"] = "application/json"
	for k, v := range r.Entry.Transport.Headers {
		h[k] = v
	}
	return media.Call{Method: http.MethodPost, URL: base + ":generateContent", Header: h, Body: raw}, nil
}

// Parse reads the inline image parts.
func (Images) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	parts := gjson.GetBytes(body, "candidates.0.content.parts")
	if !parts.Exists() {
		parts = gjson.GetBytes(body, "response.candidates.0.content.parts")
	}
	var images []string
	for _, part := range parts.Array() {
		if b := part.Get("inlineData.data").String(); b != "" {
			images = append(images, b)
		}
	}
	if len(images) == 0 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	data := make([]map[string]string, 0, len(images))
	for _, b := range images {
		data = append(data, map[string]string{"b64_json": b})
	}
	raw, _ := json.Marshal(map[string]any{"created": media.NowSec(), "data": data})
	return media.Response{Status: http.StatusOK, Header: map[string]string{"Content-Type": "application/json"}, Body: raw}, nil
}

// modelAndRatio splits an optional "-WxH" suffix off a model and picks the
// aspect ratio: the suffix, then aspect_ratio, then the requested size.
func modelAndRatio(model, size, aspect string) (string, string) {
	ratio := ""
	if i := strings.LastIndexByte(model, '-'); i > 0 {
		suffix := model[i+1:]
		if w, h, ok := pixelPair(suffix); ok {
			model = model[:i]
			ratio = reduce(w, h)
		}
	}
	if ratio == "" {
		if w, h, ok := pixelPair(aspect); ok {
			ratio = reduce(w, h)
		}
	}
	if ratio == "" {
		ratio = media.SizeToAspectRatio(size)
	}
	return model, ratio
}

// pixelPair reads "WxH" or "W:H" with both sides positive.
func pixelPair(s string) (int, int, bool) {
	sep := "x"
	if strings.Contains(s, ":") {
		sep = ":"
	}
	left, right, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), sep)
	if !ok {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(strings.TrimSpace(left))
	h, err2 := strconv.Atoi(strings.TrimSpace(right))
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return 0, 0, false
	}
	return w, h, true
}

// reduce keeps a ratio small, as the executor does: a value up to 16 is used as
// it is, anything larger is divided by its greatest common divisor.
func reduce(w, h int) string {
	if w > 16 || h > 16 {
		g := gcd(w, h)
		w, h = w/g, h/g
	}
	return strconv.Itoa(w) + ":" + strconv.Itoa(h)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}
