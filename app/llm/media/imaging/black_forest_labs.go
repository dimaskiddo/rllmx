package imaging

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// BlackForestLabs speaks FLUX's async API: a submit, then polling_url.
type BlackForestLabs struct{}

// Kind is the service it serves.
func (BlackForestLabs) Kind() string { return upstream.KindImage }

// Build makes the submit call.
func (BlackForestLabs) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	body := gjson.ParseBytes(r.Body)
	req := map[string]any{"prompt": body.Get("prompt").String()}
	if w, h := media.SizeToDimensions(body.Get("size").String()); w > 0 {
		req["width"], req["height"] = w, h
	}
	if img := body.Get("image").String(); img != "" {
		req["image_prompt"] = img
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	return media.Call{Method: http.MethodPost, URL: base + "/" + r.WireModel, Header: bflHeaders(r.Credential), Body: raw}, nil
}

// Parse polls the provider's own address until the image is ready.
func (BlackForestLabs) Parse(ctx context.Context, r media.Request, c media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	target := gjson.GetBytes(body, "polling_url").String()
	if !media.SameSite(media.BaseURL(r), target) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no job"}
	}
	out := media.Response{}
	err := media.Poll(ctx, func(ctx context.Context) (bool, error) {
		st, _, raw, err := c.Do(ctx, http.MethodGet, target, bflHeaders(r.Credential), nil)
		if err != nil {
			return false, err
		}
		if st < 200 || st > 299 {
			return false, &media.ProviderError{Status: st, Text: "the provider refused the job"}
		}
		switch gjson.GetBytes(raw, "status").String() {
		case "Ready":
			sample := gjson.GetBytes(raw, "result.sample").String()
			if sample == "" {
				return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
			}
			out = urlList([]string{sample})
			return true, nil
		case "Error", "Failed":
			return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider could not make the image"}
		}
		return false, nil
	})
	return out, err
}

func bflHeaders(credential string) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if credential != "" {
		h["x-key"] = credential
	}
	return h
}
