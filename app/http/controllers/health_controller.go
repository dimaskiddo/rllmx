package controllers

import (
	nethttp "net/http"

	"github.com/goravel/framework/contracts/http"
)

// HealthController answers the liveness route.
type HealthController struct{}

// NewHealthController builds the handler the health route serves.
func NewHealthController() *HealthController {
	return &HealthController{}
}

// Index answers 200 {"ok":true} to anyone, and no body to HEAD.
func (r *HealthController) Index(ctx http.Context) http.Response {
	if ctx.Request().Method() == nethttp.MethodHead {
		return ctx.Response().Header("Content-Type", "application/json").NoContent(nethttp.StatusOK)
	}
	return ctx.Response().Json(nethttp.StatusOK, http.Json{"ok": true})
}
