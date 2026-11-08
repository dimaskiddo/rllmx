package controllers

import (
	"io/fs"
	nethttp "net/http"

	contractshttp "github.com/goravel/framework/contracts/http"
)

// CallbackController serves the page a provider sends the browser back to and
// its script. Both are static files from the embedded dashboard folder.
type CallbackController struct {
	// Files is the embedded static folder, rooted at "static".
	Files fs.FS
}

// Page answers GET /callback.
func (c *CallbackController) Page(ctx contractshttp.Context) contractshttp.Response {
	return c.serve(ctx, "static/callback.html", "text/html; charset=utf-8")
}

// Script answers GET /static/js/callback.js.
func (c *CallbackController) Script(ctx contractshttp.Context) contractshttp.Response {
	return c.serve(ctx, "static/js/callback.js", "text/javascript; charset=utf-8")
}

func (c *CallbackController) serve(ctx contractshttp.Context, name, kind string) contractshttp.Response {
	body, err := fs.ReadFile(c.Files, name)
	if err != nil {
		return ctx.Response().String(nethttp.StatusNotFound, "Not found")
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Data(nethttp.StatusOK, kind, body)
}
