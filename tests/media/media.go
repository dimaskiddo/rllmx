// Package media gives the media adapters' tests one fake client and the byte
// fixtures they all use. It is a sub-package for the reason tests/usage is: the
// adapters' own tests may import it, and the root tests package must not import
// anything that imports it back.
package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// PNG is a one-pixel-sized PNG, enough for the format check.
var PNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

// MP4 is the head of an MP4: a box size and the ftyp marker.
var MP4 = append([]byte{0, 0, 0, 24}, append([]byte("ftypisom"), make([]byte, 24)...)...)

// JWT builds a token whose payload carries the given claims.
func JWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "x." + base64.RawURLEncoding.EncodeToString(payload) + ".y"
}

// Reply is one canned answer.
type Reply func() (status int, header http.Header, body []byte)

// Of builds a reply with no headers.
func Of(status int, body []byte) Reply {
	return func() (int, http.Header, []byte) { return status, http.Header{}, body }
}

// Routes is a media client that answers each call from the longest route whose
// text its address holds, so "chat-requirements/prepare" is not taken by
// "chat-requirements". It records every call. It is safe for concurrent use.
type Routes struct {
	mu     sync.Mutex
	routes map[string]Reply
	calls  []string
	bodies []string
}

// NewRoutes builds a client over the given routes.
func NewRoutes(routes map[string]Reply) *Routes { return &Routes{routes: routes} }

// Set adds or replaces a route.
func (r *Routes) Set(prefix string, reply Reply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[prefix] = reply
}

// Do answers one call.
func (r *Routes) Do(_ context.Context, method, target string, _ map[string]string, body []byte) (int, http.Header, []byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, method+" "+target)
	r.bodies = append(r.bodies, string(body))
	best := ""
	for prefix := range r.routes {
		if strings.Contains(target, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	reply := r.routes[best]
	r.mu.Unlock()
	if best == "" {
		return http.StatusNotFound, http.Header{}, nil, nil
	}
	status, header, out := reply()
	return status, header, out, nil
}

// DoCapped is Do: a fake has no read to bound.
func (r *Routes) DoCapped(ctx context.Context, method, target string, h map[string]string, body []byte, _ int64) (int, http.Header, []byte, error) {
	return r.Do(ctx, method, target, h, body)
}

// Calls are the "METHOD address" lines of every call, in order.
func (r *Routes) Calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// Bodies is every request body, in order, one per call.
func (r *Routes) Bodies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bodies...)
}

// Sent is every request body joined, for a test that looks for a field in any.
func (r *Routes) Sent() string { return strings.Join(r.Bodies(), "\n") }
