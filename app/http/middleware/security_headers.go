package middleware

import (
	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
)

// SecurityHeaders sets the headers every response carries. The reference sets
// none of them, because a browser only ever loads its dashboard from the same
// origin; behind a reverse proxy that is not true here.
type SecurityHeaders struct {
	// Trust decides whether the caller arrived over HTTPS, so HSTS is sent only
	// where it can be honoured and a plain-HTTP caller is never told to upgrade.
	Trust apphttp.Trust
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
	case apphttp.IsClientAPI(r.URL.Path):
	default:
		ctx.Response().Header("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		ctx.Response().Header("Referrer-Policy", "no-referrer")
	}

	if m.Trust.SecureRequest(r) {
		ctx.Response().Header("Strict-Transport-Security", "max-age=31536000")
	}
	ctx.Request().Next()
}
