// Package media carries the adapters for the non-chat services: embeddings,
// images, speech, transcription, video, search and fetch. An adapter knows one
// family's wire shape and nothing else: the gateway resolves the model, picks
// the account, refreshes it and records the request.
package media

import (
	"context"
	"net/http"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/network"
)

// Actions a video create may name.
const (
	VideoGenerations = "generations"
	VideoEdits       = "edits"
	VideoExtensions  = "extensions"
)

// Request is one media call as its adapter sees it.
type Request struct {
	Kind string
	// Provider is the canonical provider id; Model is the id the client asked
	// for; WireModel is the id or path the provider knows it by.
	Provider  string
	Model     string
	WireModel string
	Entry     upstream.Entry
	Config    *upstream.MediaConfig
	// Credential is the account's api key or access token, "" for a service
	// that takes none.
	Credential string
	// Account is the connection's provider data, for an address or an account id
	// the provider's own data names.
	Account map[string]string
	// Body is the client's parsed request, for an adapter that must reshape it.
	Body []byte
	// Upload is the file a transcription carries.
	Upload *Upload
	// ModelTransport is the per-model marker a task names (gemini-live).
	ModelTransport string
	// Action is the create a video request names: generations, edits or
	// extensions.
	Action string
	// JobID is the async job a video poll reads; empty on a create.
	JobID string
	// ContentType is the body's own media type, forwarded as the client sent it.
	ContentType string
	// IdempotencyKey is the client's key for a billable create.
	IdempotencyKey string
	// Network is the guarded dialer's configuration, for an adapter that opens a
	// socket of its own.
	Network network.Options
}

// Response is one media answer, already normalized to the client's shape.
type Response struct {
	Status int
	Header map[string]string
	Body   []byte
	// Usage is set only when the provider reported exact integer counts.
	Usage Usage
}

// Usage is the token count a media provider reported, when it reported one.
type Usage struct {
	Prompt     int64
	Completion int64
	Known      bool
}

// Client sends one call through the guarded transport. It is the same client
// every upstream call uses; an adapter that polls sends through it too.
type Client interface {
	Do(ctx context.Context, method, target string, header map[string]string, body []byte) (status int, h http.Header, respBody []byte, err error)
}

// Call is what an adapter builds: the first HTTP call of a media request.
type Call struct {
	Method string
	URL    string
	Header map[string]string
	Body   []byte
}

// Director is an adapter that runs its own exchange, for a service that is not
// one HTTP request and a parsed answer.
type Director interface {
	Direct(ctx context.Context, r Request, c Client) (Response, error)
}

// Adapter is one provider family's media wire shape.
type Adapter interface {
	Kind() string
	// Build makes the first call. An adapter that needs the provider's answer to
	// build a second call does that in Parse.
	Build(ctx context.Context, r Request, c Client) (Call, error)
	// Parse turns the provider's answer into the client's shape; it may poll
	// through c for a job the provider runs asynchronously.
	Parse(ctx context.Context, r Request, c Client, status int, header http.Header, body []byte) (Response, error)
}

// BadRequest is an adapter's refusal of a request it cannot shape. The gateway
// answers it as the client's 400 without provider text.
type BadRequest struct{ Reason string }

func (e *BadRequest) Error() string { return e.Reason }

// BadRequestf builds a refusal.
func BadRequestf(reason string) error { return &BadRequest{Reason: reason} }

// NotEntitled is a refusal that belongs to the account's plan, not to the
// request: the gateway passes over the account without reporting or locking it.
type NotEntitled struct{ Reason string }

func (e *NotEntitled) Error() string { return e.Reason }

// ProviderError is a provider answer an adapter read before the gateway could
// classify it: a status and a fixed word.
type ProviderError struct {
	Status int
	Text   string
}

func (e *ProviderError) Error() string { return e.Text }
