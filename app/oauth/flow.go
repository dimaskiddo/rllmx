// Package oauth connects provider accounts by login: it builds the authorize
// address, keeps the login in flight, exchanges the code for tokens and stores
// the account. It has no HTTP handler types; the routes live in
// app/http/controllers.
package oauth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"rllmx/app/oidc"
	"rllmx/app/upstream"
)

// Errors a caller maps to an answer. None carries provider text.
var (
	ErrUnsupported = errors.New("oauth: the provider has no login")
	ErrDisabled    = errors.New("oauth: the login is not configured")
	ErrSession     = errors.New("oauth: the login was not found, has expired or was already used")
	ErrStorage     = errors.New("oauth: storage is not available")
)

// Flow is one provider's login: its registry block and the client id that
// configuration gave it.
type Flow struct {
	Provider string
	ClientID string
	upstream.OAuth
}

// FlowFor finds the login of a provider by its name or alias and joins it with
// its client id. ErrUnsupported means no such provider.
func FlowFor(reg *upstream.Registry, clientID func(string) string, provider string) (Flow, error) {
	id, ok := reg.Resolve(provider)
	if !ok {
		return Flow{}, ErrUnsupported
	}
	e, _ := reg.ByID(id)
	return NewFlow(e, clientID(id))
}

// NewFlow joins a registry entry with its client id. An entry without a login
// block is unsupported; an empty client id means the deployer switched it off.
func NewFlow(e upstream.Entry, clientID string) (Flow, error) {
	if e.OAuth == nil {
		return Flow{}, ErrUnsupported
	}
	if clientID == "" && !e.OAuth.NoClient {
		return Flow{}, ErrDisabled
	}
	return Flow{Provider: e.ID, ClientID: clientID, OAuth: *e.OAuth}, nil
}

// RedirectURL is the address the provider sends the browser back to. The
// server decides it: a fixed port for a client registered that way, else the
// router's own port. Nothing from the request builds it but the port.
func (f Flow) RedirectURL(routerPort int) string {
	if f.Redirect == upstream.RedirectFixed {
		return fmt.Sprintf("http://localhost:%d%s", f.FixedPort, f.CallbackPath)
	}
	return fmt.Sprintf("http://localhost:%d%s", routerPort, upstream.LocalCallbackPath)
}

// ListenerPort is the port a listener must serve for this login, or 0.
func (f Flow) ListenerPort() int {
	if f.Redirect == upstream.RedirectFixed {
		return f.FixedPort
	}
	return 0
}

// URL builds the authorize address: the standard parameters, the provider's
// extra ones in file order, then the state. Spaces are written %20.
func (f Flow) URL(redirect, state, challenge string) string {
	pairs := [][2]string{
		{"response_type", "code"}, {"client_id", f.ClientID}, {"redirect_uri", redirect},
		{"scope", f.Scope}, {"code_challenge", challenge}, {"code_challenge_method", oidc.Method},
	}
	for _, p := range f.Params {
		pairs = append(pairs, [2]string{p.Name, p.Value})
	}
	pairs = append(pairs, [2]string{"state", state})
	var b strings.Builder
	b.WriteString(f.AuthorizeURL)
	for i, p := range pairs {
		if i == 0 {
			b.WriteByte('?')
		} else {
			b.WriteByte('&')
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(strings.ReplaceAll(url.QueryEscape(p[1]), "+", "%20"))
	}
	return b.String()
}
