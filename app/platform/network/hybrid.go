package network

import (
	"context"
	nethttp "net/http"
	"sync"
	"time"
)

const (
	// A host that refused QUIC is dialed over HTTP/2 directly for this long, so
	// a provider that does not serve it is not tried on every cold connection.
	http3RetryAfter = 5 * time.Minute
	http3CacheMax   = 256
)

// http3Cache remembers the hosts that failed over QUIC. Transport copies made
// for a request-scoped timeout share one cache.
type http3Cache struct {
	mu   sync.Mutex
	dead map[string]time.Time
}

func (c *http3Cache) usable(host string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.dead[host]
	if !ok {
		return true
	}
	if time.Now().Before(until) {
		return false
	}
	delete(c.dead, host)
	return true
}

func (c *http3Cache) mark(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// hybridTransport tries HTTP/3 and falls back to HTTP/2 on any failure. The
// original request body is never handed to the HTTP/3 attempt, so a fallback
// sends it whole.
type hybridTransport struct {
	h2    nethttp.RoundTripper
	h3    nethttp.RoundTripper
	cache *http3Cache
}

type closeIdler interface {
	CloseIdleConnections()
}

func (h *hybridTransport) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	if !h.useHTTP3(req) {
		return h.h2.RoundTrip(req)
	}
	h3req := req.Clone(req.Context())
	if req.Body != nil {
		// A body that cannot be copied is sent over HTTP/2 only: the HTTP/3
		// attempt would consume the only copy.
		if req.GetBody == nil {
			return h.h2.RoundTrip(req)
		}
		body, err := req.GetBody()
		if err != nil {
			return h.h2.RoundTrip(req)
		}
		h3req.Body = body
	}
	resp, err := h.h3.RoundTrip(h3req)
	if err == nil {
		return resp, nil
	}
	if req.Context().Err() != nil {
		return nil, err
	}
	h.cache.mark(req.URL.Host)
	return h.h2.RoundTrip(req)
}

func (h *hybridTransport) useHTTP3(req *nethttp.Request) bool {
	if req.URL.Scheme != "https" || noHTTP3(req.Context()) {
		return false
	}
	return h.cache.usable(req.URL.Host)
}

// WithResponseHeaderTimeout clones the HTTP/2 side with the provider's bound.
// HTTP/3 has no such knob; its whole exchange stays bounded by the client.
func (h *hybridTransport) WithResponseHeaderTimeout(d time.Duration) nethttp.RoundTripper {
	base, ok := h.h2.(*nethttp.Transport)
	if !ok {
		return h
	}
	clone := base.Clone()
	clone.ResponseHeaderTimeout = d
	return &hybridTransport{h2: clone, h3: h.h3, cache: h.cache}
}

func (h *hybridTransport) CloseIdleConnections() {
	if c, ok := h.h2.(closeIdler); ok {
		c.CloseIdleConnections()
	}
	if c, ok := h.h3.(closeIdler); ok {
		c.CloseIdleConnections()
	}
}
