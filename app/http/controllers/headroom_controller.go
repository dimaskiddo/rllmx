package controllers

import (
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/gateway/saver/headroom"
	"rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

// Headroom sidecar bounds.
const (
	headroomProbeWait   = 1500 * time.Millisecond
	headroomProbeLength = 4 << 10
	maxHeadroomBody     = 8 << 20
)

// errEmptySidecar refuses a stored address that is not a plain http(s) URL.
var errEmptySidecar = errors.New("controllers: the sidecar address is not usable")

// HeadroomController answers the sidecar's dashboard routes: a reachability
// probe and a strict reverse proxy. The router never installs, starts or stops
// the sidecar, and the address always comes from the operator's setting.
type HeadroomController struct {
	DB func() *repository.Store
	// Client builds the guarded HTTP client for one sidecar host.
	Client func(host string) *nethttp.Client
	// DefaultURL is the configured address used when the settings row holds none.
	DefaultURL string

	mu      sync.Mutex
	clients map[string]*nethttp.Client
}

// headroomStatus is the answer to GET /api/headroom/status.
type headroomStatus struct {
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
}

// Status answers GET /api/headroom/status with the configured address and
// whether it answers its health route.
func (c *HeadroomController) Status(ctx contractshttp.Context) contractshttp.Response {
	target, ok := c.target(ctx)
	if !ok {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The settings are not available")
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, headroomStatus{URL: maskURL(target), Reachable: c.probe(ctx, target)})
}

// probe reports whether the sidecar answers /health within the bound.
func (c *HeadroomController) probe(ctx contractshttp.Context, base string) bool {
	pctx, cancel := context.WithTimeout(ctx.Request().Origin().Context(), headroomProbeWait)
	defer cancel()
	req, err := nethttp.NewRequestWithContext(pctx, nethttp.MethodGet, strings.TrimSuffix(base, "/")+"/health", nil)
	if err != nil {
		return false
	}
	cl := c.client(base)
	if cl == nil {
		return false
	}
	res, err := cl.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, headroomProbeLength))
	return res.StatusCode >= 200 && res.StatusCode <= 299
}

// Proxy forwards one dashboard request to the sidecar and relays the answer. It
// strips hop-by-hop headers, never forwards the router's own session cookie or
// key to any sidecar, and does not follow a redirect.
func (c *HeadroomController) Proxy(ctx contractshttp.Context) contractshttp.Response {
	origin := ctx.Request().Origin()
	w := ctx.Response().Writer()
	base, ok := c.target(ctx)
	if !ok {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The settings are not available")
	}
	target, err := sidecarURL(base, ctx.Request().Route("path"), origin.URL.RawQuery)
	if err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "The sidecar address is not usable")
	}

	out, err := nethttp.NewRequestWithContext(origin.Context(), origin.Method, target, proxyBody(origin))
	if err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "The request cannot be forwarded")
	}
	copyProxyHeaders(out.Header, origin.Header)
	out.Host = ""
	cl := c.client(target)
	if cl == nil {
		return flat(ctx, nethttp.StatusServiceUnavailable, "The sidecar address is not usable")
	}
	res, err := cl.Do(out)
	if err != nil {
		return flat(ctx, nethttp.StatusBadGateway, "The sidecar did not answer")
	}
	defer func() { _ = res.Body.Close() }()
	copyProxyHeaders(w.Header(), res.Header)
	// The sidecar's page runs on the dashboard's origin, so it is boxed in: no
	// script, no form, no sniffing into another type.
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Del("Content-Length")
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(res.Body, maxHeadroomBody))
	return nil
}

// target is the sidecar's base address from the settings row, then the
// configuration, then the default.
func (c *HeadroomController) target(ctx contractshttp.Context) (string, bool) {
	st := storeOf(c.DB)
	if st == nil {
		return "", false
	}
	d, err := st.Settings().Effective(ctx.Request().Origin().Context())
	if err != nil {
		return "", false
	}
	return text.FirstNonEmpty(strings.TrimSpace(d.Str("headroomUrl", "")), strings.TrimSpace(c.DefaultURL), headroom.DefaultURL), true
}

// client is the guarded client for the sidecar's host, built once per host: a
// new one per call would leave an idle pool behind on every status and proxy.
func (c *HeadroomController) client(target string) *nethttp.Client {
	if c.Client == nil {
		return nil
	}
	u, err := url.Parse(target)
	if err != nil {
		return nil
	}
	host := u.Hostname()
	c.mu.Lock()
	defer c.mu.Unlock()
	if made, ok := c.clients[host]; ok {
		return made
	}
	made := c.Client(host)
	if c.clients == nil {
		c.clients = map[string]*nethttp.Client{}
	}
	c.clients[host] = made
	return made
}

// proxyBody streams the request body under a cap, or nothing for a method that
// carries none.
func proxyBody(r *nethttp.Request) io.Reader {
	switch r.Method {
	case nethttp.MethodGet, nethttp.MethodHead, nethttp.MethodDelete:
		return nil
	}
	return io.LimitReader(r.Body, maxHeadroomBody)
}

// hopHeaders never travel through a proxy.
var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// credentialHeaders never cross the proxy in either direction: the router's own
// session and key go nowhere, and a sidecar may not set a cookie on the
// dashboard's origin.
var credentialHeaders = map[string]bool{"Cookie": true, "Authorization": true, "Set-Cookie": true}

// copyProxyHeaders copies headers minus the hop-by-hop and credential sets.
func copyProxyHeaders(dst, src nethttp.Header) {
	for k, vals := range src {
		if hopHeaders[k] || credentialHeaders[k] {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// sidecarURL joins the sidecar's base address with the forwarded path and query.
func sidecarURL(base, path, query string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return "", errEmptySidecar
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + strings.TrimPrefix(path, "/")
	u.RawQuery = query
	return u.String(), nil
}

// maskURL removes any user info before an address is shown, so a stored
// credential never reaches the dashboard.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User = nil
	u.Fragment = ""
	return u.String()
}
