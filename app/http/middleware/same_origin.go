package middleware

import (
	nethttp "net/http"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/failure"
	apphttp "rllmx/app/http"
)

// samlACS is the one cross-site POST the product answers, so the only path the
// cross-site guard lets through. The IdP posts to it from its own origin.
const samlACS = "/api/auth/saml/acs"

// SameOrigin refuses a state-changing dashboard call sent from another site.
// The session cookie is `SameSite=Lax`, which already keeps it off a cross-site
// POST; this is the second gate, and it also covers a browser that sends the
// cookie anyway.
type SameOrigin struct {
	// Exempt holds exact paths that accept a cross-site write by design.
	Exempt []string
}

// Signature is how the stack names this middleware.
func (m *SameOrigin) Signature() string {
	return "SameOrigin"
}

// Handle refuses a cross-site write. A request carrying neither Origin nor
// Sec-Fetch-Site is a script or a CLI, not a browser, and passes.
func (m *SameOrigin) Handle(ctx contractshttp.Context) {
	r := ctx.Request().Origin()
	if !stateChanging(r.Method) || !apphttp.IsDashboardAPI(r.URL.Path) || m.exempt(r.URL.Path) {
		ctx.Request().Next()
		return
	}

	if ok := sameOrigin(r); !ok {
		failure.WriteFor(ctx.Response().Writer(), r, nethttp.StatusForbidden, "Cross-site request refused")
		ctx.Request().Abort(nethttp.StatusForbidden)
		return
	}
	ctx.Request().Next()
}

func (m *SameOrigin) exempt(path string) bool {
	for _, p := range m.Exempt {
		if p == path {
			return true
		}
	}
	return false
}

func stateChanging(method string) bool {
	switch method {
	case nethttp.MethodPost, nethttp.MethodPut, nethttp.MethodPatch, nethttp.MethodDelete:
		return true
	}
	return false
}

// sameOrigin decides from the fetch metadata the browser sends. A repeated
// header is never trusted, and `same-site` is refused: another port of this
// machine is same-site, not same-origin, and gets the cookie.
func sameOrigin(r *nethttp.Request) bool {
	if sites := r.Header.Values("Sec-Fetch-Site"); len(sites) > 0 {
		if len(sites) > 1 {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(sites[0])) {
		case "same-origin", "none":
			return true
		}
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) > 1 {
		return false
	}
	host, ok := apphttp.OriginHost(origins[0])
	return ok && host != "" && host == strings.ToLower(r.Host)
}
