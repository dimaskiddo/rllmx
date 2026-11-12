package imaging

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Stability speaks Stability's v2beta generate API. The reference sends JSON
// here; v2beta takes multipart, so this adapter does.
type Stability struct{}

// Kind is the service it serves.
func (Stability) Kind() string { return upstream.KindImage }

// Build makes the generation call.
func (Stability) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	segment := "core"
	switch {
	case strings.Contains(r.WireModel, "ultra"):
		segment = "ultra"
	case strings.Contains(r.WireModel, "sd3"):
		segment = "sd3"
	}
	body := gjson.ParseBytes(r.Body)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("prompt", body.Get("prompt").String())
	format := strings.ToLower(strings.TrimSpace(firstText(body, "output_format", "response_format")))
	if format == "" {
		format = "png"
	}
	_ = mw.WriteField("output_format", format)
	if size := body.Get("size").String(); size != "" {
		_ = mw.WriteField("aspect_ratio", media.SizeToAspectRatio(size))
	}
	if style := body.Get("style").String(); style != "" {
		_ = mw.WriteField("style_preset", style)
	}
	if segment == "sd3" {
		_ = mw.WriteField("model", r.WireModel)
	}
	if err := mw.Close(); err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := map[string]string{"Accept": "application/json", "Content-Type": mw.FormDataContentType()}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: base + "/" + segment, Header: h, Body: buf.Bytes()}, nil
}

// Parse reads the one image the answer holds.
func (Stability) Parse(_ context.Context, _ media.Request, _ media.Client, status int, _ http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	image := gjson.GetBytes(body, "image").String()
	if image == "" {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
	}
	return imageList([]string{image}), nil
}
