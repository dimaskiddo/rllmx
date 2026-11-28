package controllers

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	nethttp "net/http"
	"path"
	"strings"
	"sync"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/platform/text"
)

// PagesController serves the dashboard's embedded files: the page a provider
// sends the browser back to, and everything under /static.
type PagesController struct {
	// Files is the embedded web folder, holding "static/...".
	Files fs.FS

	etags sync.Map
}

// assetTypes is every extension /static serves. A file type outside it is a
// 404 even when it is embedded, so a stray source or note never goes out.
var assetTypes = map[string]string{
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".mjs":  "text/javascript; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
	".ico":  "image/x-icon",
	".html": "text/html; charset=utf-8",
}

// assetName maps the wildcard of GET /static/*file to a name inside the embedded
// folder. A page template is the one HTML a script may fetch, and only from
// pages/, so the shell and the sign-in page stay behind their own routes.
func assetName(file string) (name, kind string, ok bool) {
	file = strings.TrimPrefix(file, "/")
	if file == "" || !fs.ValidPath(file) {
		return "", "", false
	}
	for seg := range strings.SplitSeq(file, "/") {
		if strings.HasPrefix(seg, ".") || strings.ContainsAny(seg, "\\:") {
			return "", "", false
		}
	}
	if text.HasASCIIControl(file) {
		return "", "", false
	}
	kind, ok = assetTypes[strings.ToLower(path.Ext(file))]
	if !ok || (strings.HasSuffix(file, ".html") && !strings.HasPrefix(file, "pages/")) {
		return "", "", false
	}
	return "static/" + file, kind, true
}

// Root answers GET /: the dashboard is the only entry point.
func (c *PagesController) Root(ctx contractshttp.Context) contractshttp.Response {
	return ctx.Response().Redirect(nethttp.StatusFound, "/dashboard")
}

// Callback answers GET /callback. The page carries a one-time code, so it is
// never stored.
func (c *PagesController) Callback(ctx contractshttp.Context) contractshttp.Response {
	return c.page(ctx, "static/callback.html", "no-store")
}

// Login answers GET /login.
func (c *PagesController) Login(ctx contractshttp.Context) contractshttp.Response {
	return c.page(ctx, "static/login.html", "no-cache")
}

// Dashboard answers GET /dashboard and every path under it: one shell, and the
// router in it picks the page.
func (c *PagesController) Dashboard(ctx contractshttp.Context) contractshttp.Response {
	return c.page(ctx, "static/shell.html", "no-cache")
}

// page serves one embedded HTML file with the policy's content type. The ETag
// lets an open browser revalidate after a build replaces the binary.
func (c *PagesController) page(ctx contractshttp.Context, name, cacheControl string) contractshttp.Response {
	return c.serve(ctx, name, "text/html; charset=utf-8", cacheControl)
}

// Asset answers GET /static/*file. The answer is revalidated on every load: the
// files ship inside the binary, so a new build must reach an open browser.
func (c *PagesController) Asset(ctx contractshttp.Context) contractshttp.Response {
	name, kind, ok := assetName(ctx.Request().Route("file"))
	if !ok {
		return ctx.Response().String(nethttp.StatusNotFound, "Not found")
	}
	return c.serve(ctx, name, kind, "no-cache")
}

// serve answers one embedded file. The folder is embedded, so a file's tag never
// changes while the process runs: once it is known, a browser that already holds
// the file is answered from the tag without opening the file again.
func (c *PagesController) serve(ctx contractshttp.Context, name, kind, cacheControl string) contractshttp.Response {
	ctx.Response().Header("Cache-Control", cacheControl)
	if tag, ok := c.knownTag(name); ok && matches(ctx.Request().Header("If-None-Match"), tag) {
		ctx.Response().Header("ETag", tag)
		return ctx.Response().NoContent(nethttp.StatusNotModified)
	}
	body, err := fs.ReadFile(c.Files, name)
	if err != nil {
		ctx.Response().Header("Cache-Control", "no-store")
		return ctx.Response().String(nethttp.StatusNotFound, "Not found")
	}
	tag := c.etag(name, body)
	ctx.Response().Header("ETag", tag)
	if matches(ctx.Request().Header("If-None-Match"), tag) {
		return ctx.Response().NoContent(nethttp.StatusNotModified)
	}
	return ctx.Response().Data(nethttp.StatusOK, kind, body)
}

// knownTag is the tag a file got when it was first served, if it has been.
func (c *PagesController) knownTag(name string) (string, bool) {
	v, ok := c.etags.Load(name)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// etag hashes a file once and remembers the tag.
func (c *PagesController) etag(name string, body []byte) string {
	if tag, ok := c.knownTag(name); ok {
		return tag
	}
	sum := sha256.Sum256(body)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	c.etags.Store(name, tag)
	return tag
}

// matches reports whether an If-None-Match list holds the tag, weak or strong.
func matches(header, tag string) bool {
	for v := range strings.SplitSeq(header, ",") {
		if v = strings.TrimPrefix(strings.TrimSpace(v), "W/"); v == tag || v == "*" {
			return true
		}
	}
	return false
}
