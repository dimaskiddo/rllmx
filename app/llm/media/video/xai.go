package video

import (
	"context"
	"net/http"
	"net/url"

	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
)

// XAI is xAI's async job shape, which the other providers vary from: a create
// at {base}/{action}, a poll at {base}/{id}, the body and the answer passed
// through untouched.
type XAI struct{}

// Kind is the service it serves.
func (XAI) Kind() string { return upstream.KindVideo }

// Build makes the create or the poll call.
func (XAI) Build(_ context.Context, r media.Request, _ media.Client) (media.Call, error) {
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
	if r.ContentType != "" {
		h["Content-Type"] = r.ContentType
	}
	if r.IdempotencyKey != "" {
		h["Idempotency-Key"] = r.IdempotencyKey
	}
	return media.Call{Method: http.MethodPost, URL: media.WithQuery(b+"/"+r.Action, q), Header: h, Body: r.Body}, nil
}

// Parse passes the provider's answer through.
func (XAI) Parse(_ context.Context, _ media.Request, _ media.Client, status int, header http.Header, body []byte) (media.Response, error) {
	return forwarded(status, header, body, "the provider refused the video")
}
