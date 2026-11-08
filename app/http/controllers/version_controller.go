package controllers

import (
	nethttp "net/http"

	"github.com/goravel/framework/contracts/http"
)

// VersionController answers the version route. It never contacts a package
// registry: the updater is out of scope, and an unreachable registry must not
// show up as an error on a public route.
type VersionController struct {
	version string
	commit  string
}

// NewVersionController builds the handler the version route serves.
func NewVersionController(version, commit string) *VersionController {
	return &VersionController{version: version, commit: commit}
}

// Index answers the version the binary was built with. latestVersion is null and
// hasUpdate false, the shape the reference answers when its lookup fails.
func (r *VersionController) Index(ctx http.Context) http.Response {
	body := http.Json{
		"currentVersion": r.version,
		"commit":         r.commit,
		"latestVersion":  nil,
		"hasUpdate":      false,
	}
	if ctx.Request().Method() == nethttp.MethodHead {
		return ctx.Response().Header("Content-Type", "application/json").NoContent(nethttp.StatusOK)
	}
	return ctx.Response().Json(nethttp.StatusOK, body)
}
