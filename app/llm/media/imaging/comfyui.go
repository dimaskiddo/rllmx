package imaging

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// ComfyUI posts the prompt to the local daemon and passes its answer through.
type ComfyUI struct{}

// Kind is the service it serves.
func (ComfyUI) Kind() string { return upstream.KindImage }

// Build makes the submit call.
func (ComfyUI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	base := media.BaseURL(r)
	if base == "" {
		return media.Call{}, media.BadRequestf("the provider names no address")
	}
	payload, err := json.Marshal(map[string]string{"prompt": gjson.GetBytes(r.Body, "prompt").String()})
	if err != nil {
		return media.Call{}, media.BadRequestf("the request cannot be built")
	}
	return media.Call{Method: http.MethodPost, URL: base, Header: map[string]string{"Content-Type": "application/json"}, Body: payload}, nil
}

// Parse passes the daemon's JSON through.
func (ComfyUI) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	return media.PassJSON(status, header, body, "the provider answered no image")
}
