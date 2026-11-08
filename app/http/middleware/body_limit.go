package middleware

import (
	nethttp "net/http"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/failure"
	apphttp "rllmx/app/http"
)

// apiBodyBytes is the cap on every body that is not a client API call. A
// dashboard JSON body is small; the client API carries images and audio, so it
// gets the operator's own cap instead.
const apiBodyBytes = 16 << 20

// BodyLimit bounds a request body before the handler reads it. A declared
// length over the cap is refused at once; a chunked body stops at the cap, so
// neither an announced nor a hidden upload can spend unbounded memory.
type BodyLimit struct {
	// ClientMax is the client API cap in bytes. Zero means no cap.
	ClientMax int64
}

// Signature is how the stack names this middleware.
func (m *BodyLimit) Signature() string {
	return "BodyLimit"
}

// Handle refuses an oversized body and wraps the rest.
func (m *BodyLimit) Handle(ctx contractshttp.Context) {
	r := ctx.Request().Origin()
	limit := int64(apiBodyBytes)
	if apphttp.IsClientAPI(r.URL.Path) {
		limit = m.ClientMax
	}
	if limit <= 0 {
		ctx.Request().Next()
		return
	}

	if r.ContentLength > limit {
		failure.WriteFor(ctx.Response().Writer(), r, nethttp.StatusRequestEntityTooLarge, "")
		ctx.Request().Abort(nethttp.StatusRequestEntityTooLarge)
		return
	}
	if r.Body != nil && r.Body != nethttp.NoBody {
		r.Body = nethttp.MaxBytesReader(ctx.Response().Writer(), r.Body, limit)
	}
	ctx.Request().Next()
}
