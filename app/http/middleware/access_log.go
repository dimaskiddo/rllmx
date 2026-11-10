package middleware

import (
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/platform/text"
)

const maxLoggedPath = 200

// AccessLog writes one line per request: method, path, status, bytes, time and
// request id. The path is the one the router saw, never the query string, and
// control characters are removed so a path cannot forge a log line. Headers,
// cookies and bodies are never logged.
type AccessLog struct {
	Log log.Log
}

// Signature is how the stack names this middleware.
func (m *AccessLog) Signature() string {
	return "AccessLog"
}

// Handle logs the path, status, size and request id once the next handler returns.
func (m *AccessLog) Handle(ctx contractshttp.Context) {
	start := time.Now()
	ctx.Request().Next()

	r := ctx.Request().Origin()
	status, size := 0, 0
	if o := ctx.Response().Origin(); o != nil {
		status, size = o.Status(), max(o.Size(), 0)
	}
	m.Log.Infof("%s %s %d %dB %s %s", r.Method, text.CutRunes(text.StripControl(r.URL.Path), maxLoggedPath),
		status, size, time.Since(start).Round(time.Microsecond), RequestIDFrom(ctx))
}
