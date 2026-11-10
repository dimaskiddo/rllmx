package google

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
)

// Vertex hosts and defaults.
const (
	vertexHost           = "https://aiplatform.googleapis.com/v1"
	vertexTokenLocation  = "us-central1"
	vertexRawLocation    = "global"
	vertexPartnerPath    = "/locations/global/endpoints/openapi/chat/completions"
	vertexProjectsPrefix = "/projects/"
)

// ErrTokenUnreachable is what a TokenSource returns when it could not ask Google
// at all, as against being refused: the account is not at fault.
var ErrTokenUnreachable = errors.New("executor: the token endpoint could not be reached")

// TokenSource gives the bearer token for a Vertex key. A failure it cannot
// separate from a refused key is any other error.
type TokenSource interface {
	Token(ctx context.Context, key upstream.CloudKey) (string, error)
}

// Vertex sends a request to Vertex AI. The key is a service-account file, user
// credentials or a plain API key; the first two are traded for a bearer token and
// the third goes in a header, never in the address. The address carries the
// project and location when the key or the account names them. With Partner set it
// sends an OpenAI body to the partner-model endpoint.
type Vertex struct {
	Default *executor.Default
	Tokens  TokenSource
	Partner bool
}

// Execute sends one call. req.Key holds the key text.
func (v *Vertex) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	if req.Key == "" {
		return executor.Response{}, executor.ErrNoCredential
	}
	key, ok := upstream.ParseCloudKey(req.Key)
	if !ok {
		return executor.Response{}, executor.ErrBadAccount
	}
	project, location := v.place(req, key)
	target, err := v.address(req, key, project, location)
	if err != nil {
		return executor.Response{}, err
	}
	req.URL = target
	auth := req.Auth
	if auth == nil {
		auth = req.Entry.Auth
	}
	req.Auth = &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}
	if key.Kind == upstream.KeyRaw {
		req.Auth = &upstream.Auth{Scheme: upstream.SchemeRaw, Header: "x-goog-api-key"}
		req.Key = strings.TrimSpace(req.Key)
	} else {
		if v.Tokens == nil {
			return executor.Response{}, executor.ErrBadAccount
		}
		token, err := v.Tokens.Token(ctx, key)
		if err != nil {
			return executor.Response{}, tokenFailed(err)
		}
		req.Key = token
	}
	if auth != nil && len(auth.Headers) > 0 {
		req.Auth.Headers = auth.Headers
	}
	return v.Default.Execute(ctx, req)
}

// place picks the project and the location: the account's own data first, then
// what the key names, then the model's default.
func (v *Vertex) place(req executor.Request, key upstream.CloudKey) (project, location string) {
	if req.Account != nil {
		project, location = req.Account("projectId"), req.Account("location")
	}
	if !upstream.ValidProject(project) {
		project = key.Project
	}
	if !upstream.ValidLocation(location) {
		location = ""
	}
	return project, location
}

// address builds the endpoint. A key that is a token source always needs a
// project; a raw key uses the project when the account has one and the open
// endpoint otherwise.
func (v *Vertex) address(req executor.Request, key upstream.CloudKey, project, location string) (string, error) {
	if v.Partner {
		if !upstream.ValidProject(project) {
			return "", executor.ErrBadAccount
		}
		return vertexHost + vertexProjectsPrefix + project + vertexPartnerPath, nil
	}
	action := ":generateContent"
	if req.Stream {
		action = ":streamGenerateContent?alt=sse"
	}
	model := url.PathEscape(req.Model)
	if key.Kind != upstream.KeyRaw && !upstream.ValidProject(project) {
		return "", executor.ErrBadAccount
	}
	if !upstream.ValidProject(project) {
		return vertexHost + "/publishers/google/models/" + model + action, nil
	}
	if location == "" {
		location = req.Spec.DefaultLocation
	}
	if !upstream.ValidLocation(location) {
		location = vertexTokenLocation
		if key.Kind == upstream.KeyRaw {
			location = vertexRawLocation
		}
	}
	return vertexHost + vertexProjectsPrefix + project + "/locations/" + location + "/publishers/google/models/" + model + action, nil
}

// tokenFailed maps a failed token request: being unable to reach Google is the
// network's failure and the next try may work, anything else means the key is not
// good.
func tokenFailed(err error) error {
	if errors.Is(err, ErrTokenUnreachable) || errors.Is(err, context.DeadlineExceeded) {
		return executor.ErrUnreachable
	}
	return executor.ErrBadAccount
}
