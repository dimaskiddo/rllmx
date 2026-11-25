package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"strings"
	"sync"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
)

// ReleasesURL is the newest release of this repository. It is a constant, like
// the catalog a sync falls back to, so no request can name another address.
const ReleasesURL = "https://api.github.com/repos/dimaskiddo/rllmx/releases/latest"

// aboutTimeout bounds the one outbound call the About page can make.
const aboutTimeout = 8 * time.Second

// maxReleaseField bounds a field read from the release answer.
const maxReleaseField = 256

// AboutController answers the update check the About page asks for. The check
// runs only when a reader asks: nothing here polls on a page load.
type AboutController struct {
	Version string
	Commit  string
	// Fetch reads the release answer through the guarded client; nil answers 503.
	Fetch func(ctx context.Context, address string) ([]byte, error)
	// Upgrade installs the newest release and answers its version and the path
	// of the swapped binary; nil leaves the action out.
	Upgrade func(ctx context.Context) (latest, exe string, err error)
	// Restart replaces this process with the binary at exe; nil keeps it as it
	// is, for a build that cannot restart itself.
	Restart func(exe string)

	// installing refuses a second install while one runs: two would race on the
	// binary's rename and could leave none on disk.
	installing sync.Mutex
}

// release is the part of GitHub's answer the page shows.
type release struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
}

// Update answers GET /api/about/update with the newest release beside this build.
func (c *AboutController) Update(ctx contractshttp.Context) contractshttp.Response {
	ctx.Response().Header("Cache-Control", "no-store")
	if c.Fetch == nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The update check is not available")
	}
	rc, cancel := context.WithTimeout(ctx.Request().Origin().Context(), aboutTimeout)
	defer cancel()
	body, err := c.Fetch(rc, ReleasesURL)
	if err != nil {
		return flat(ctx, nethttp.StatusBadGateway, "Could not reach GitHub")
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return flat(ctx, nethttp.StatusBadGateway, "Could not reach GitHub")
	}
	latest := versionOf(rel.TagName)
	if latest == "" {
		return flat(ctx, nethttp.StatusBadGateway, "Could not reach GitHub")
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"latestVersion": latest,
		"url":           cutField(rel.HTMLURL),
		"publishedAt":   cutField(rel.PublishedAt),
		"hasUpdate":     latest != versionOf(c.Version) && versionOf(c.Version) != "dev",
	})
}

// RunUpgrade answers POST /api/about/upgrade: it installs the newest release and
// then replaces the process with it. The answer leaves first, so the caller is
// not left waiting for a connection that is about to end.
func (c *AboutController) RunUpgrade(ctx contractshttp.Context) contractshttp.Response {
	ctx.Response().Header("Cache-Control", "no-store")
	if c.Upgrade == nil || c.Restart == nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The update action is not available")
	}
	if !c.installing.TryLock() {
		return flat(ctx, nethttp.StatusConflict, "An update is already being installed")
	}
	defer c.installing.Unlock()
	rc, cancel := context.WithTimeout(ctx.Request().Origin().Context(), 5*time.Minute)
	defer cancel()
	latest, exe, err := c.Upgrade(rc)
	if err != nil {
		return flat(ctx, nethttp.StatusBadGateway, "The update could not be installed")
	}
	time.AfterFunc(300*time.Millisecond, func() { c.Restart(exe) })
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"latestVersion": latest, "restarting": true})
}

// versionOf trims a tag and cuts the "v" a release tag carries, so one spelling
// of the same version compares equal.
func versionOf(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxReleaseField {
		v = v[:maxReleaseField]
	}
	return strings.TrimPrefix(v, "v")
}

// cutField bounds what the page shows, so a hostile answer cannot fill it.
func cutField(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxReleaseField {
		v = v[:maxReleaseField]
	}
	return v
}
