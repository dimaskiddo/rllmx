package imaging

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// Fal speaks fal's queue API.
type Fal struct{}

// Kind is the service it serves.
func (Fal) Kind() string { return upstream.KindImage }

// Build makes the submit call.
func (Fal) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	body := gjson.ParseBytes(r.Body)
	req := map[string]any{"prompt": body.Get("prompt").String(), "num_images": 1}
	if n, ok := intField(body, "n"); ok && n > 0 && n <= 10 {
		req["num_images"] = n
	}
	if w, h := media.SizeToDimensions(body.Get("size").String()); w > 0 {
		req["image_size"] = map[string]int{"width": w, "height": h}
	}
	if img := body.Get("image").String(); img != "" {
		req["image_url"] = img
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	return media.Call{Method: http.MethodPost, URL: base + "/" + r.WireModel, Header: falHeaders(r.Credential), Body: raw}, nil
}

// Parse polls the queue, then reads the result.
func (Fal) Parse(ctx context.Context, r media.Request, c media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	statusURL := gjson.GetBytes(body, "status_url").String()
	responseURL := gjson.GetBytes(body, "response_url").String()
	base := media.BaseURL(r)
	if !media.SameSite(base, statusURL) || !media.SameSite(base, responseURL) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no job"}
	}
	var out media.Response
	err := media.Poll(ctx, func(ctx context.Context) (bool, error) {
		st, _, raw, err := c.Do(ctx, http.MethodGet, statusURL, falHeaders(r.Credential), nil)
		if err != nil {
			return false, err
		}
		if st < 200 || st > 299 {
			return false, &media.ProviderError{Status: st, Text: "the provider refused the job"}
		}
		switch gjson.GetBytes(raw, "status").String() {
		case "COMPLETED":
			rs, _, result, err := c.Do(ctx, http.MethodGet, responseURL, falHeaders(r.Credential), nil)
			if err != nil {
				return false, err
			}
			if rs < 200 || rs > 299 {
				return false, &media.ProviderError{Status: rs, Text: "the provider refused the result"}
			}
			images := gjson.GetBytes(result, "images")
			var urls []string
			if images.IsArray() {
				for _, img := range images.Array() {
					if u := firstText(img, "url"); u != "" {
						urls = append(urls, u)
					} else if img.Type == gjson.String {
						urls = append(urls, img.String())
					}
				}
			} else if u := gjson.GetBytes(result, "image.url").String(); u != "" {
				urls = append(urls, u)
			}
			if len(urls) == 0 {
				return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
			}
			out = urlList(urls)
			return true, nil
		case "FAILED":
			return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider could not make the image"}
		}
		return false, nil
	})
	return out, err
}

func falHeaders(credential string) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if credential != "" {
		h["Authorization"] = "Key " + credential
	}
	return h
}
