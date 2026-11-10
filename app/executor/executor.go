// Package executor sends one request to one upstream account and returns the
// answer unread. It knows how a provider is addressed and how a key is
// presented; what the answer means is the pipeline's business.
package executor

import (
	"context"
	"errors"
	"io"
	"net/http"

	"rllmx/app/upstream"
)

// ErrNoCredential means the connection holds nothing to authenticate with.
var ErrNoCredential = errors.New("executor: the connection has no credential")

// Request is one call to an upstream.
type Request struct {
	URL    string
	Method string
	// Body is the translated request body.
	Body []byte
	// Stream asks for an event stream, which adds the Accept header.
	Stream bool
	// Entry is the provider's registry entry; zero for a user node.
	Entry upstream.Entry
	// Key is the credential to present. It is read from the connection by the
	// caller at the last moment and is never stored, logged or put in an error.
	Key string
	// Overrides are the operator's extra headers for this provider.
	Overrides map[string]string
	// Auth, when set, overrides Entry.Auth: user nodes carry no registry entry.
	Auth *upstream.Auth
	// Model is the upstream model id.
	Model string
	// Spec is the model's registry row; zero when the provider does not list it.
	Spec upstream.Model
	// Session is the conversation id sent upstream: the keyed hash bound to the
	// account, never a value a client chose.
	Session string
	// Compact marks a call to the context-compaction route.
	Compact bool
	// Levels are the reasoning efforts the model accepts; empty means any.
	Levels []string
	// Account reads a text field of the account's provider specific data; nil
	// reads nothing.
	Account func(name string) string
	// Sign signs the request once its headers are assembled, adding whatever
	// proof the provider wants. A signer that fails stops the call: an unsigned
	// request would be refused anyway, and sending it costs a round trip.
	Sign func(method, target string, h http.Header, body []byte) error
	// NoRedirect refuses a redirect instead of following it, for a request that
	// carries a credential no other origin may be handed.
	NoRedirect bool
	// Renamed, when set, is told the tools this one call had to send under
	// another name, as a map of the sent name to the caller's. It is called
	// before the request goes out.
	Renamed func(map[string]string)
}

// Response is the upstream's answer, not yet read. The caller closes Body.
type Response struct {
	Status int
	Header http.Header
	Body   io.ReadCloser
}

// Executor sends a request.
type Executor interface {
	Execute(ctx context.Context, req Request) (Response, error)
}
