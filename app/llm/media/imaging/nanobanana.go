package imaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// NanoBanana submits a task and polls its record.
type NanoBanana struct{}

// Kind is the service it serves.
func (NanoBanana) Kind() string { return upstream.KindImage }

// Build makes the submit call.
func (NanoBanana) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" || r.Config == nil || r.Config.PollURL == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	body := gjson.ParseBytes(r.Body)
	var sources []string
	if img := body.Get("image").String(); img != "" {
		sources = append(sources, img)
	}
	for _, v := range body.Get("images").Array() {
		if v.Type == gjson.String && v.String() != "" {
			sources = append(sources, v.String())
		}
	}
	kind := "TEXTTOIAMGE"
	if len(sources) > 0 {
		kind = "IMAGETOIAMGE"
	}
	req := map[string]any{
		"prompt":      body.Get("prompt").String(),
		"type":        kind,
		"numImages":   1,
		"image_size":  media.SizeToAspectRatio(body.Get("size").String()),
		"callBackUrl": "https://localhost/callback",
	}
	if n, ok := intField(body, "n"); ok && n > 0 && n <= 10 {
		req["numImages"] = n
	}
	if len(sources) > 0 {
		req["imageUrls"] = sources
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	return media.Call{Method: http.MethodPost, URL: base, Header: h, Body: raw}, nil
}

// Parse polls the record until the image is ready.
func (NanoBanana) Parse(ctx context.Context, r media.Request, c media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	if gjson.GetBytes(body, "code").Int() != 200 {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider refused the image"}
	}
	task := gjson.GetBytes(body, "data.taskId").String()
	if task == "" || r.Config == nil || !media.SameSite(media.BaseURL(r), r.Config.PollURL) {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no job"}
	}
	target := r.Config.PollURL + "?taskId=" + url.QueryEscape(task)
	h := map[string]string{"Accept": "application/json"}
	if r.Credential != "" {
		h["Authorization"] = "Bearer " + r.Credential
	}
	var out media.Response
	err := media.Poll(ctx, func(ctx context.Context) (bool, error) {
		st, _, raw, err := c.Do(ctx, http.MethodGet, target, h, nil)
		if err != nil {
			return false, err
		}
		if st < 200 || st > 299 {
			return false, &media.ProviderError{Status: st, Text: "the provider refused the job"}
		}
		switch int(gjson.GetBytes(raw, "data.successFlag").Int()) {
		case 1:
			u := gjson.GetBytes(raw, "data.response.resultImageUrl").String()
			if u == "" {
				u = gjson.GetBytes(raw, "data.response.originImageUrl").String()
			}
			if u == "" {
				return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
			}
			out = urlList([]string{u})
			return true, nil
		case 2, 3:
			return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider could not make the image"}
		}
		return false, nil
	})
	return out, err
}
