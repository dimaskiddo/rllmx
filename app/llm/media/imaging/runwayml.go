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

// Runway speaks its v1 tasks API for image and video models.
type Runway struct{}

// Kind is the service it serves.
func (Runway) Kind() string { return upstream.KindImage }

// Build makes the submit call.
func (Runway) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := strings.TrimRight(media.BaseURL(r), "/")
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	body := gjson.ParseBytes(r.Body)
	ratio := media.PixelRatio(body.Get("size").String())
	prompt := body.Get("prompt").String()
	if runwayVideo(r) {
		// The reference serves a video model through the images route, on the
		// image-to-video task, with a fixed five-second clip.
		req := map[string]any{"promptText": prompt, "model": r.WireModel, "ratio": ratio, "duration": 5}
		if img := body.Get("image").String(); img != "" {
			req["promptImage"] = img
		}
		raw, err := json.Marshal(req)
		if err != nil {
			return media.Call{}, media.BadRequestf("the request cannot be built")
		}
		return media.Call{Method: http.MethodPost, URL: base + "/image_to_video", Header: runwayHeaders(r.Credential), Body: raw}, nil
	}
	req := map[string]any{
		"promptText": prompt,
		"model":      r.WireModel,
		"ratio":      ratio,
	}
	if img := body.Get("image").String(); img != "" {
		req["referenceImages"] = []map[string]string{{"uri": img}}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	return media.Call{Method: http.MethodPost, URL: base + "/text_to_image", Header: runwayHeaders(r.Credential), Body: raw}, nil
}

// runwayVideo reports whether the model is one of the provider's video models.
// The id is what the provider knows it by; the client's own name may carry a
// provider prefix.
func runwayVideo(r media.Request) bool {
	id := r.WireModel
	if id == "" {
		id = r.Model
	}
	if _, rest, ok := strings.Cut(id, "/"); ok {
		id = rest
	}
	for _, m := range r.Entry.Models {
		if m.ID == id && m.Kind == upstream.KindVideo {
			return true
		}
	}
	return false
}

// Parse polls the task until it succeeds.
func (Runway) Parse(ctx context.Context, r media.Request, c media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	if status < 200 || status > 299 {
		return media.Response{}, &media.ProviderError{Status: status, Text: "the provider refused the image"}
	}
	id := gjson.GetBytes(body, "id").String()
	if id == "" || strings.ContainsAny(id, "/?#") {
		return media.Response{}, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no job"}
	}
	base := strings.TrimRight(media.BaseURL(r), "/")
	var out media.Response
	err := media.Poll(ctx, func(ctx context.Context) (bool, error) {
		st, _, raw, err := c.Do(ctx, http.MethodGet, base+"/tasks/"+id, runwayHeaders(r.Credential), nil)
		if err != nil {
			return false, err
		}
		if st < 200 || st > 299 {
			return false, &media.ProviderError{Status: st, Text: "the provider refused the job"}
		}
		switch gjson.GetBytes(raw, "status").String() {
		case "SUCCEEDED":
			var urls []string
			for _, v := range gjson.GetBytes(raw, "output").Array() {
				if v.Type == gjson.String && v.String() != "" {
					urls = append(urls, v.String())
				}
			}
			if len(urls) == 0 {
				return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider answered no image"}
			}
			out = urlList(urls)
			return true, nil
		case "FAILED", "CANCELLED":
			return false, &media.ProviderError{Status: http.StatusBadGateway, Text: "the provider could not make the image"}
		}
		return false, nil
	})
	return out, err
}

func runwayHeaders(credential string) map[string]string {
	h := map[string]string{"Content-Type": "application/json", "Accept": "application/json", "X-Runway-Version": "2024-11-06"}
	if credential != "" {
		h["Authorization"] = "Bearer " + credential
	}
	return h
}
