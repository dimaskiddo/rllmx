package middleware

import (
	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/captcha"
	apphttp "rllmx/app/http"
)

// SecurityHeaders sets the headers every response carries. The reference sets
// none of them, because a browser only ever loads its dashboard from the same
// origin; behind a reverse proxy that is not true here. A page and the dashboard
// API get the full set; the client API gets only what leaves its open CORS alone.
type SecurityHeaders struct {
	// Trust decides whether the caller arrived over HTTPS, so HSTS is sent only
	// where it can be honoured and a plain-HTTP caller is never told to upgrade.
	Trust apphttp.Trust
	// LoginPolicy is pagePolicy plus the CAPTCHA provider's hosts, sent to the
	// sign-in page only. Empty leaves every page on pagePolicy.
	LoginPolicy string
}

const (
	// pagePolicy lets a page load only from its own origin and forbids what a
	// dashboard never needs: a base address, a plug-in object, a form that posts
	// elsewhere and being framed. Scripts and styles stay on the origin with no
	// inline code. Images may also be data: URIs, because the stylesheet draws
	// its checkboxes, selects and chevrons from inline SVG.
	pagePolicy = "default-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; object-src 'none'; frame-ancestors 'none'"
	// permissions turns off every browser capability the dashboard has no use for.
	permissions = "camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()"
)

// LoginPolicy is the sign-in page's policy while a CAPTCHA provider is set: the
// page policy plus the hosts the provider's script and frame need. An empty
// provider returns "".
func LoginPolicy(provider string) string { return captcha.Policy(pagePolicy, provider) }

// isolate keeps a page and the dashboard API to their own origin: no other
// window shares its browsing context, no other site embeds its answers, and no
// frame holds it. The client API is left out, because its CORS is open on
// purpose and a cross-origin policy would close it again.
func isolate(ctx contractshttp.Context) {
	ctx.Response().Header("Permissions-Policy", permissions)
	ctx.Response().Header("Cross-Origin-Opener-Policy", "same-origin")
	ctx.Response().Header("Cross-Origin-Resource-Policy", "same-origin")
	ctx.Response().Header("X-Frame-Options", "DENY")
}

// Signature is how the stack names this middleware.
func (m *SecurityHeaders) Signature() string {
	return "SecurityHeaders"
}

// Handle sets the headers before the handler runs, so they are present on a
// success, an error and a stream alike.
func (m *SecurityHeaders) Handle(ctx contractshttp.Context) {
	r := ctx.Request().Origin()
	ctx.Response().Header("X-Content-Type-Options", "nosniff")

	switch {
	case apphttp.IsDashboardAPI(r.URL.Path):
		ctx.Response().Header("Cache-Control", "no-store")
		isolate(ctx)
	case apphttp.IsClientAPI(r.URL.Path):
	case r.URL.Path == "/login" && m.LoginPolicy != "":
		ctx.Response().Header("Content-Security-Policy", m.LoginPolicy)
		ctx.Response().Header("Referrer-Policy", "no-referrer")
		isolate(ctx)
	default:
		ctx.Response().Header("Content-Security-Policy", pagePolicy)
		ctx.Response().Header("Referrer-Policy", "no-referrer")
		isolate(ctx)
	}

	if m.Trust.SecureRequest(r) {
		ctx.Response().Header("Strict-Transport-Security", "max-age=31536000")
	}
	ctx.Request().Next()
}
