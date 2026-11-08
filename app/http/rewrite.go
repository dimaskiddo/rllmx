package http

import (
	nethttp "net/http"
	"path"
	"strings"

	"rllmx/app/failure"
)

// Rewrite maps client alias paths onto router routes before routing and refuses
// non-canonical paths, so the guard and the router never read a path differently.
// It wraps Route().ServeHTTP because the router matches before its middleware.
func Rewrite(next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		p := r.URL.Path
		if !canonical(p, r.URL.RawPath) {
			failure.WriteFor(w, r, nethttp.StatusBadRequest, "Invalid request path")
			return
		}
		if to, ok := rewriteTarget(p); ok {
			r2 := new(nethttp.Request)
			*r2 = *r
			u := *r.URL
			u.Path, u.RawPath = to, ""
			r2.URL = &u
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}

func rewriteTarget(p string) (string, bool) {
	switch {
	case p == "/v1/v1":
		return "/v1", true
	case strings.HasPrefix(p, "/v1/v1/"):
		return p[len("/v1"):], true
	case p == "/codex" || strings.HasPrefix(p, "/codex/"):
		return "/v1/responses", true
	case p == "/responses":
		return "/v1/responses", true
	case p == "/systemone":
		return "/v1/systemone", true
	}
	return "", false
}

// canonical reports whether a path is already in the one form the router
// matches: absolute, cleaned, no trailing slash (except the root), no control
// characters or backslashes, and no encoded slash, dot or NUL in the raw form.
func canonical(p, raw string) bool {
	if p == "" || p[0] != '/' || path.Clean(p) != p {
		return false
	}
	for i := 0; i < len(p); i++ {
		if c := p[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return false
		}
	}
	if raw != "" {
		l := strings.ToLower(raw)
		for _, bad := range []string{"%2f", "%5c", "%2e", "%00"} {
			if strings.Contains(l, bad) {
				return false
			}
		}
	}
	return true
}
