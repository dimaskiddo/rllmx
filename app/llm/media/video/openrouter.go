package video

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// OpenRouter posts a create to the collection root, not to an action, and has
// no edits or extensions; its answer passes through as xAI's does.
type OpenRouter struct{}

// Kind is the service it serves.
func (OpenRouter) Kind() string { return upstream.KindVideo }

// Build makes the create or the poll call.
func (OpenRouter) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
	if err := checkCreate(r); err != nil {
		return media.Call{}, err
	}
	b, err := base(r)
	if err != nil {
		return media.Call{}, err
	}
	q := url.Values{}
	h := media.Headers(r, q)
	if r.JobID != "" {
		id, err := jobPath(r.JobID)
		if err != nil {
			return media.Call{}, err
		}
		return media.Call{Method: http.MethodGet, URL: media.WithQuery(b+"/"+id, q), Header: h}, nil
	}
	if r.Action != media.VideoGenerations {
		return media.Call{}, media.BadRequestf("this provider serves generations only")
	}
	if ct := r.ContentType; ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
		return media.Call{}, media.BadRequestf("this provider takes a JSON body")
	}
	h["Content-Type"] = "application/json"
	return media.Call{Method: http.MethodPost, URL: media.WithQuery(b, q), Header: h, Body: r.Body}, nil
}

// Parse passes the provider's answer through.
func (OpenRouter) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	return forwarded(status, header, body, "the provider refused the video")
}
