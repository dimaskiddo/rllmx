package identity

import (
	nethttp "net/http"
	"strings"
)

// ExtractKey returns the client key a request presents, or "". The order is the
// one PRD 2 fixes: `Authorization: Bearer`, then `x-api-key` on every client
// route, and `x-goog-api-key` then `?key=` only under /v1beta. The first present
// value wins, so a client cannot mix two and have the second read.
func ExtractKey(r *nethttp.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		if rest, ok := strings.CutPrefix(h, "Bearer "); ok {
			if key := strings.TrimSpace(rest); key != "" {
				return key
			}
		}
	}
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		return key
	}
	if !strings.HasPrefix(r.URL.Path, "/v1beta") {
		return ""
	}
	if key := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); key != "" {
		return key
	}
	return strings.TrimSpace(r.URL.Query().Get("key"))
}
