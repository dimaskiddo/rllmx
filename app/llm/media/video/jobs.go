// Package video carries the adapters for the video endpoints: a create posts one
// billable job, a poll reads it back. The xAI shape is the one the others are
// written against: a create at {base}/{action}, a poll at {base}/{id}, and the
// provider's own JSON passed through.
package video

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"rllmx/app/llm/media"
)

// actions are the creates a client may name.
var actions = map[string]bool{
	media.VideoGenerations: true, media.VideoEdits: true, media.VideoExtensions: true,
}

// jobIDPattern is a job id fit for one path segment. A provider's id can carry a
// slash (a resource path), so an id outside this shape is refused rather than
// escaped into the address.
var jobIDPattern = regexp.MustCompile(`^[A-Za-z0-9._~:-]{1,512}$`)

// base is the service address, or a refusal when the provider names none.
func base(r media.Request) (string, error) {
	b := strings.TrimRight(media.BaseURL(r), "/")
	if b == "" {
		return "", media.BadRequestf("the provider names no address")
	}
	return b, nil
}

// jobPath is one job id as one safe path segment.
func jobPath(id string) (string, error) {
	if !jobIDPattern.MatchString(id) {
		return "", media.BadRequestf("the job id is not one this router accepts")
	}
	return url.PathEscape(id), nil
}

// forwarded is the upstream answer in the client's shape: the provider's JSON as
// it arrived, with its own media type.
func forwarded(status int, header http.Header, body []byte, refusal string) (media.Response, error) {
	return media.PassJSON(status, header, body, refusal)
}

// checkCreate refuses an action a provider does not serve.
func checkCreate(r media.Request) error {
	if r.JobID != "" {
		return nil
	}
	if !actions[r.Action] {
		return media.BadRequestf("the request names no video action")
	}
	return nil
}
