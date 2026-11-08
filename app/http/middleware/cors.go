package middleware

import (
	nethttp "net/http"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
)

const (
	// exposedHeaders are what a browser client may read off a client API answer:
	// the request id for support, Retry-After for a backoff, and whether the
	// response cache answered.
	exposedHeaders = "X-Request-Id, Retry-After, X-Rllm-Cache"
	allowMethods   = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	// defaultHeaders cover every SDK header; a wildcard does not include
	// Authorization, so the list has to be explicit when the client names none.
	defaultHeaders = "Authorization, Content-Type, X-Api-Key, X-Goog-Api-Key, Anthropic-Version, Anthropic-Beta, X-Request-Id, X-Rllm-Cache"
	maxEchoBytes   = 1024
	maxAgeSeconds  = "86400"
)

// CORS opens the client API to any origin and leaves the dashboard API alone,
// because a browser loads the dashboard from its own origin. Only an OPTIONS
// that names the method it asks about is a preflight; any other OPTIONS goes
// through the guard like every other request.
type CORS struct{}

// Signature is how the stack names this middleware.
func (m *CORS) Signature() string {
	return "CORS"
}

// Handle adds the headers and answers a preflight before the guard sees it.
func (m *CORS) Handle(ctx contractshttp.Context) {
	r := ctx.Request().Origin()
	if !apphttp.IsClientAPI(r.URL.Path) {
		ctx.Request().Next()
		return
	}

	ctx.Response().Header("Access-Control-Allow-Origin", "*")
	ctx.Response().Header("Access-Control-Expose-Headers", exposedHeaders)

	if r.Method == nethttp.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
		ctx.Response().Header("Access-Control-Allow-Methods", allowMethods)
		ctx.Response().Header("Access-Control-Allow-Headers", echoHeaders(r.Header.Get("Access-Control-Request-Headers")))
		ctx.Response().Header("Access-Control-Max-Age", maxAgeSeconds)
		ctx.Response().Header("Vary", "Access-Control-Request-Headers")
		ctx.Request().Abort(nethttp.StatusNoContent)
		return
	}
	ctx.Request().Next()
}

// echoHeaders returns the requested header names when they are only header-name
// characters, else the default list. Echoing keeps a client's own header working
// without opening the door to a header injection.
func echoHeaders(requested string) string {
	if requested == "" || len(requested) > maxEchoBytes {
		return defaultHeaders
	}
	for i := 0; i < len(requested); i++ {
		switch c := requested[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == ',', c == ' ':
		default:
			return defaultHeaders
		}
	}
	return requested
}
