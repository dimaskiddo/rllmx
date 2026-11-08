package oauth

import (
	"time"

	"rllmx/app/dialect"
	"rllmx/app/document"
)

// timeOf reads a stored time, in the router's own form or any RFC 3339 one.
func timeOf(d document.Doc, key string) (time.Time, bool) {
	s, ok := d.GetString(key)
	if !ok || s == "" {
		return time.Time{}, false
	}
	if t, err := dialect.ParseTimestamp(s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// Due reports whether a connection's access token should be renewed now: it has
// a refresh token, and it expires within the flow's lead time or, for a flow
// with an age rule, was last renewed too long ago. A token with no known expiry
// and no age rule is left alone; a refused request renews it.
func Due(f Flow, d document.Doc, now time.Time) bool {
	if rt, _ := d.GetString("refreshToken"); rt == "" {
		return false
	}
	lead := time.Duration(f.Refresh.LeadSeconds) * time.Second
	if exp, ok := timeOf(d, "expiresAt"); ok && exp.Sub(now) < lead {
		return true
	}
	if f.Refresh.MaxAgeSeconds > 0 {
		last, ok := timeOf(d, "lastRefreshAt")
		return !ok || now.Sub(last) >= time.Duration(f.Refresh.MaxAgeSeconds)*time.Second
	}
	return false
}
