package controllers

import (
	"context"
	nethttp "net/http"

	"github.com/goravel/framework/contracts/http"
)

// InitController answers the setup-state route the dashboard calls at load.
type InitController struct {
	// connections counts the configured providers. nil means unknown.
	connections func(ctx context.Context) (int64, error)
}

// NewInitController builds the handler the init route serves.
func NewInitController(connections func(ctx context.Context) (int64, error)) *InitController {
	return &InitController{connections: connections}
}

// Index reports whether any provider is configured, so the dashboard can send a
// fresh install to the setup wizard. An unreadable count answers "no setup
// needed" rather than turning a broken store into a setup prompt.
func (r *InitController) Index(ctx http.Context) http.Response {
	needsSetup := false
	if r.connections != nil {
		n, err := r.connections(ctx.Request().Origin().Context())
		needsSetup = err == nil && n == 0
	}
	body := http.Json{"initialized": true, "needsSetup": needsSetup}
	if ctx.Request().Method() == nethttp.MethodHead {
		return ctx.Response().Header("Content-Type", "application/json").NoContent(nethttp.StatusOK)
	}
	return ctx.Response().Json(nethttp.StatusOK, body)
}
