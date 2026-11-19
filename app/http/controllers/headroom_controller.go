package controllers

import (
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"net/url"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/store/repository"
)

// Headroom sidecar bounds.
const (
	headroomDefaultURL  = "http://localhost:8787"
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
	res, err := c.client(base).Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, headroomProbeLength))
	return res.StatusCode >= 200 && res.StatusCode <= 299
}

// Proxy forwards one dashboard request to the sidecar and relays the answer. It
// strips hop-by-hop headers, never forwards the router's own session cookie to a
// sidecar that is not on this machine, and does not follow a redirect.
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
	copyProxyHeaders(out.Header, origin.Header, isLoopback(target))
	out.Host = ""
	res, err := c.client(target).Do(out)
	if err != nil {
		return flat(ctx, nethttp.StatusBadGateway, "The sidecar did not answer")
	}
	defer func() { _ = res.Body.Close() }()
	copyProxyHeaders(w.Header(), res.Header, true)
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
	for _, v := range []string{rawString(d, "headroomUrl", ""), c.DefaultURL, headroomDefaultURL} {
		if strings.TrimSpace(v) != "" {
			return v, true
		}
	}
	return headroomDefaultURL, true
}

func (c *HeadroomController) client(target string) *nethttp.Client {
	if c.Client == nil {
		return nethttp.DefaultClient
	}
	u, err := url.Parse(target)
	if err != nil {
		return nethttp.DefaultClient
	}
	return c.Client(u.Hostname())
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

// copyProxyHeaders copies headers minus the hop-by-hop set. Credentials and the
// router's own session cookie are withheld unless the target is on this machine.
func copyProxyHeaders(dst, src nethttp.Header, withCredentials bool) {
	for k, vals := range src {
		if hopHeaders[k] {
			continue
		}
		if !withCredentials && (k == "Cookie" || k == "Authorization") {
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

// isLoopback reports a sidecar on this machine, where forwarding the caller's
// own credentials is no wider than the caller's request.
func isLoopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
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
