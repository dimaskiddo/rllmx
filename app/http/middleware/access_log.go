package middleware

import (
	"fmt"
	"net/http"
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

// quiet reports whether a method's line belongs at DEBUG. A read, such as a
// dashboard poll or a page load, is routine; a call that changes state is the
// kind of event the log is kept for.
func quiet(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
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
	line := fmt.Sprintf("request id=%s method=%s path=%s status=%d bytes=%d dur=%s",
		RequestIDFrom(ctx), r.Method, text.CutRunes(text.StripControl(r.URL.Path), maxLoggedPath),
		status, size, time.Since(start).Round(time.Microsecond))
	if quiet(r.Method) {
		m.Log.Debugf("%s", line)
		return
	}
	m.Log.Infof("%s", line)
}
