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
	"rllmx/app/sso"
	"rllmx/app/upstream"
)

// Errors a caller maps to an answer. None carries provider text.
var (
	ErrUnsupported = errors.New("oauth: the provider has no login")
	ErrDisabled    = errors.New("oauth: the login is not configured")
	ErrSession     = errors.New("oauth: the login was not found, has expired or was already used")
	ErrStorage     = errors.New("oauth: storage is not available")
	ErrVariant     = errors.New("oauth: this login needs one of the choices its provider offers")
)

// Flow is one provider's login: its registry block and the client id that
// configuration gave it.
type Flow struct {
	Provider string
	ClientID string
	// ClientSecret is the secret of the client: from configuration for a provider
	// whose client has one (Google), or the one of an application that came with
	// the account. Empty means the exchange and the renewal send none.
	ClientSecret string
	// AppBase is the server address of that application, or "".
	AppBase string
	upstream.OAuth
}

// App is an OAuth application that comes with a login request: the server it
// lives on, its id and, when it has one, its secret. It is held in memory for
// the length of the login and sealed with the account.
type App struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
}

const maxAppField = 256

// Valid reports an application fit to log in with: a bare https origin and an
// id and secret that are short and free of spaces and control characters.
func (a App) Valid() bool {
	return upstream.BareOrigin(a.BaseURL) && appText(a.ClientID, true) && appText(a.ClientSecret, false)
}

func appText(s string, required bool) bool {
	if s == "" {
		return !required
	}
	return len(s) <= maxAppField && !strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// WithApp returns the flow for an application that came with the account: its
// own id and secret, and every provider address moved onto its server. Only a
// login whose data asks for it takes one.
func (f Flow) WithApp(a App) (Flow, error) {
	if !f.AppFromAccount || !a.Valid() {
		return Flow{}, ErrUnsupported
	}
	f.ClientID, f.ClientSecret, f.AppBase = a.ClientID, a.ClientSecret, a.BaseURL
	f.AuthorizeURL = rebase(f.AuthorizeURL, a.BaseURL)
	f.TokenURL = rebase(f.TokenURL, a.BaseURL)
	f.Refresh.URL = rebase(f.Refresh.URL, a.BaseURL)
	f.Endpoints = rebaseAll(f.Endpoints, a.BaseURL)
	return f, nil
}

// rebaseAll moves every endpoint address onto another server.
func rebaseAll(eps map[string]string, base string) map[string]string {
	out := make(map[string]string, len(eps))
	for k, v := range eps {
		out[k] = rebase(v, base)
	}
	return out
}

// rebase puts the path and query of a data address on another server.
func rebase(addr, base string) string {
	if addr == "" {
		return ""
	}
	u, err := url.Parse(addr)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(base, "/") + u.EscapedPath()
}

// FlowFor finds the login of a provider by its name or alias and joins it with
// its client id and, when configuration holds one, its client secret. ErrUnsupported means no such provider.
func FlowFor(reg *upstream.Registry, clientID, clientSecret func(string) string, provider string) (Flow, error) {
	id, ok := reg.Resolve(provider)
	if !ok {
		return Flow{}, ErrUnsupported
	}
	e, _ := reg.ByID(id)
	f, err := NewFlow(e, clientID(id))
	if err == nil && clientSecret != nil {
		f.ClientSecret = clientSecret(id)
	}
	return f, err
}

// NewFlow joins a registry entry with its client id. An entry without a login
// block is unsupported; an empty client id means the deployer switched it off.
func NewFlow(e upstream.Entry, clientID string) (Flow, error) {
	if e.OAuth == nil {
		return Flow{}, ErrUnsupported
	}
	if clientID == "" && !e.OAuth.NoClient && !e.OAuth.AppFromAccount {
		return Flow{}, ErrDisabled
	}
	return Flow{Provider: e.ID, ClientID: clientID, OAuth: *e.OAuth}, nil
}

// RedirectURL is the address the provider sends the browser back to. The
// server decides it: a fixed port for a client registered that way, the public
// address of this gateway for one registered with it, else the router's own
// port. Nothing from the request builds it but the port and the public base,
// which the caller took from configuration or a local host.
func (f Flow) RedirectURL(routerPort int, publicBase string) string {
	switch f.Redirect {
	case upstream.RedirectFixed:
		host := f.RedirectHost
		if host == "" {
			host = "localhost"
		}
		return fmt.Sprintf("http://%s:%d%s", host, f.FixedPort, f.CallbackPath)
	case upstream.RedirectPublic:
		return sso.CleanURL(publicBase, upstream.LocalCallbackPath)
	}
	return fmt.Sprintf("http://localhost:%d%s", routerPort, upstream.LocalCallbackPath)
}

// NeedsPublicBase reports a login whose redirect is the gateway's public address.
func (f Flow) NeedsPublicBase() bool { return f.Redirect == upstream.RedirectPublic }

// ListenerPort is the port a listener must serve for this login, or 0.
func (f Flow) ListenerPort() int {
	if f.Redirect == upstream.RedirectFixed {
		return f.FixedPort
	}
	return 0
}

// URL builds the authorize address. A code-pkce login sends the standard
// parameters, the provider's extra ones in file order, then the state; a code
// login sends only the provider's own parameters. Spaces are written %20.
//
// A dialect whose provider owns the redirect scheme builds its own address:
// Kiro's provider only answers a custom scheme, so its parameters are not the
// standard ones and cannot be reached by editing this list.
// SignedIn reports a login that makes its own key, whose address and verifier
// therefore depend on each other.
func (f Flow) SignedIn() bool { return f.Dialect == zedDialect }

// URL is the address the browser is sent to. verifier carries what the login
// made for itself, for a sign-in whose address depends on it.
func (f Flow) URLWith(redirect, state, challenge, nonce, verifier string) string {
	if f.Dialect == kiroSocialDialect {
		return kiroSocialAuthorize(f, state, challenge)
	}
	var pairs [][2]string
	if f.Flow != upstream.FlowCode {
		pairs = [][2]string{
			{"response_type", "code"}, {"client_id", f.ClientID}, {"redirect_uri", redirect},
			{"scope", f.Scope},
		}
		if !f.NoPKCE {
			pairs = append(pairs, [2]string{"code_challenge", challenge}, [2]string{"code_challenge_method", oidc.Method})
		}
	}
	fill := strings.NewReplacer(upstream.RedirectToken, redirect, upstream.StateToken, state, upstream.NonceToken, nonce)
	for _, p := range f.Params {
		pairs = append(pairs, [2]string{p.Name, fill.Replace(p.Value)})
	}
	if f.Flow != upstream.FlowCode {
		pairs = append(pairs, [2]string{"state", state})
	}
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

// URL is the address the browser is sent to, for a login that keeps nothing of
// its own.
func (f Flow) URL(redirect, state, challenge, nonce string) string {
	return f.URLWith(redirect, state, challenge, nonce, "")
}
