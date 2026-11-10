package middleware

import (
	"fmt"
	nethttp "net/http"
	"runtime/debug"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/platform/failure"
	"rllmx/app/platform/text"
)

const (
	maxPanicText  = 500
	maxPanicStack = 4000
)

// Recover handles a panic in a handler. It logs the panic value, the path and
// the request id, plus the stack, and never request data. Before the response
// has started it answers 500 in the client's format. After that it re-panics
// http.ErrAbortHandler, so the client sees a broken stream instead of one that
// looks complete. The framework's own recover handler logs the whole request.
func Recover(l log.Log) func(ctx contractshttp.Context, err any) {
	return func(ctx contractshttp.Context, err any) {
		if err == nethttp.ErrAbortHandler {
			panic(err)
		}
		r := ctx.Request().Origin()
		l.Errorf("panic serving %s %s %s: %s\n%s", r.Method, text.CutRunes(text.StripControl(r.URL.Path), maxLoggedPath),
			RequestIDFrom(ctx), text.CutRunes(text.StripControl(fmt.Sprint(err)), maxPanicText), text.CutBytes(string(debug.Stack()), maxPanicStack))

		if w, ok := ctx.Response().Writer().(interface{ Written() bool }); ok && w.Written() {
			panic(nethttp.ErrAbortHandler)
		}
		failure.WriteFor(ctx.Response().Writer(), r, nethttp.StatusInternalServerError, "")
		ctx.Request().Abort(nethttp.StatusInternalServerError)
	}
}
