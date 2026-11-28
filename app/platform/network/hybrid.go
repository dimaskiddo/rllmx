package network

import (
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// A host that failed over QUIC is dialed over HTTP/2 directly for this long,
	// even if it keeps advertising h3.
	http3RetryAfter = 5 * time.Minute
	http3CacheMax   = 256
)

// http3Cache holds what the client knows about each host's QUIC service: the
// hosts that advertised h3 and the ones that failed over QUIC. A host is tried
// over QUIC only once it advertised, so a provider on HTTP/1.1 or HTTP/2 never
// waits out a QUIC handshake. Transport copies made for a request-scoped timeout
// share one cache.
type http3Cache struct {
	mu   sync.Mutex
	alt  map[string]time.Time
	dead map[string]time.Time
}

func newHTTP3Cache() *http3Cache {
	return &http3Cache{alt: map[string]time.Time{}, dead: map[string]time.Time{}}
}

func (c *http3Cache) usable(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if until, ok := c.dead[host]; ok {
		if time.Now().Before(until) {
			return false
		}
		delete(c.dead, host)
	}
	until, ok := c.alt[host]
	if !ok {
		return false
	}
	if !time.Now().Before(until) {
		delete(c.alt, host)
		return false
	}
	return true
}

// learn records an advertisement. A host marked dead stays unusable until its
// mark runs out, however often it advertises.
func (c *http3Cache) learn(host string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.alt) >= http3CacheMax {
		clear(c.alt)
	}
	c.alt[host] = until
}

func (c *http3Cache) forget(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.alt, host)
}

func (c *http3Cache) mark(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.alt, host)
	if len(c.dead) >= http3CacheMax {
		clear(c.dead)
	}
	c.dead[host] = time.Now().Add(http3RetryAfter)
}

// noHTTP3Key marks a request that must not try QUIC.
type noHTTP3Key struct{}

// WithoutHTTP3 makes this request go over HTTP/2 only. A provider known to
// refuse QUIC uses it rather than paying a failed dial on every cold connection.
func WithoutHTTP3(ctx context.Context) context.Context {
	return context.WithValue(ctx, noHTTP3Key{}, true)
}

func noHTTP3(ctx context.Context) bool {
	skip, _ := ctx.Value(noHTTP3Key{}).(bool)
	return skip
}

// hybridTransport tries HTTP/3 for a host that advertised it and falls back to HTTP/2 when the HTTP/3 attempt
// failed before the request was written. After that the upstream may already
// hold the call, so the failure is returned and a second send is left to the
// caller's own retry rules. The original request body is never handed to the
// HTTP/3 attempt, so a fallback sends it whole.
type hybridTransport struct {
	h2    nethttp.RoundTripper
	h3    nethttp.RoundTripper
	cache *http3Cache
	// headerBound caps the wait for HTTP/3 response headers, as the HTTP/2
	// transport's ResponseHeaderTimeout does for its own; zero is no cap.
	headerBound time.Duration
}

type closeIdler interface {
	CloseIdleConnections()
}

// headerTimeout is the error of an HTTP/3 attempt whose headers never came.
type headerTimeout struct{}

func (headerTimeout) Error() string   { return "network: timeout awaiting HTTP/3 response headers" }
func (headerTimeout) Timeout() bool   { return true }
func (headerTimeout) Temporary() bool { return true }

// releaseOnClose ends the attempt's context when its body is closed, so the
// bound that guarded the headers never cuts an answer that already started.
type releaseOnClose struct {
	io.ReadCloser
	release context.CancelFunc
}

func (b releaseOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.release()
	return err
}

func (h *hybridTransport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	resp, err := h.roundTrip(req)
	if err == nil && resp != nil && req.URL.Scheme == "https" {
		h.observe(req, resp)
	}
	return resp, err
}

// observe reads the answer's Alt-Svc. Only an https answer is believed, since a
// header read over clear text could be forged to steer the next dial.
func (h *hybridTransport) observe(req *nethttp.Request, resp *nethttp.Response) {
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	for _, v := range resp.Header.Values("Alt-Svc") {
		ttl, clear, ok := parseAltSvc(v, port)
		switch {
		case !ok:
			continue
		case clear:
			h.cache.forget(req.URL.Host)
		default:
			h.cache.learn(req.URL.Host, time.Now().Add(ttl))
		}
		return
	}
}

func (h *hybridTransport) roundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	if !h.useHTTP3(req) {
		return h.h2.RoundTrip(req)
	}
	body := req.Body
	if body != nil {
		// A body that cannot be copied is sent over HTTP/2 only: the HTTP/3
		// attempt would consume the only copy.
		if req.GetBody == nil {
			return h.h2.RoundTrip(req)
		}
		var err error
		if body, err = req.GetBody(); err != nil {
			return h.h2.RoundTrip(req)
		}
	}
	var wrote atomic.Bool
	ctx := httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				wrote.Store(true)
			}
		},
	})
	release, disarm := func() {}, func() {}
	var expired atomic.Bool
	if h.headerBound > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		timer := time.AfterFunc(h.headerBound, func() {
			expired.Store(true)
			cancel()
		})
		disarm = func() { timer.Stop() }
		release = func() {
			timer.Stop()
			cancel()
		}
	}
	h3req := req.Clone(ctx)
	h3req.Body = body
	resp, err := h.h3.RoundTrip(h3req)
	if err == nil {
		disarm()
		if h.headerBound > 0 {
			resp.Body = releaseOnClose{ReadCloser: resp.Body, release: release}
		}
		return resp, nil
	}
	release()
	if req.Context().Err() != nil {
		return nil, err
	}
	h.cache.mark(req.URL.Host)
	if wrote.Load() {
		if expired.Load() {
			return nil, headerTimeout{}
		}
		return nil, err
	}
	return h.h2.RoundTrip(req)
}

func (h *hybridTransport) useHTTP3(req *nethttp.Request) bool {
	if req.URL.Scheme != "https" || noHTTP3(req.Context()) {
		return false
	}
	return h.cache.usable(req.URL.Host)
}

// WithResponseHeaderTimeout gives both sides the provider's bound: the HTTP/2
// transport through its own field, HTTP/3 through headerBound.
func (h *hybridTransport) WithResponseHeaderTimeout(d time.Duration) nethttp.RoundTripper {
	base, ok := h.h2.(*nethttp.Transport)
	if !ok {
		return h
	}
	clone := base.Clone()
	clone.ResponseHeaderTimeout = d
	return &hybridTransport{h2: clone, h3: h.h3, cache: h.cache, headerBound: d}
}

func (h *hybridTransport) CloseIdleConnections() {
	if c, ok := h.h2.(closeIdler); ok {
		c.CloseIdleConnections()
	}
	if c, ok := h.h3.(closeIdler); ok {
		c.CloseIdleConnections()
	}
}
