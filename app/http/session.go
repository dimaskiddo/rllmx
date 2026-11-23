package http

import (
	nethttp "net/http"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
)

// sessionKey is where the guard keeps who a request acts as, for the dashboard
// controllers that shape their answer by role.
type sessionKey struct{}

// WithSession records the signed-in caller. The guard calls it for every
// request it lets through with a session; a test calls it to stand in.
func WithSession(ctx contractshttp.Context, s auth.Session) {
	ctx.WithValue(sessionKey{}, s)
}

// SessionFrom returns the signed-in caller and their role.
func SessionFrom(ctx contractshttp.Context) (auth.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(auth.Session)
	return s, ok
}

// userRoutes is everything a standard user may reach: their own session, their
// own keys and the usage of those keys. Everything else is the admin's.
var userRoutes = map[string]bool{
	nethttp.MethodGet + " /api/auth/status":            true,
	nethttp.MethodPost + " /api/auth/logout":           true,
	nethttp.MethodPost + " /api/auth/change-password":  true,
	nethttp.MethodGet + " /api/settings/require-login": true,
	nethttp.MethodGet + " /api/keys":                   true,
	nethttp.MethodGet + " /api/usage/stats":            true,
	nethttp.MethodGet + " /api/usage/chart":            true,
	nethttp.MethodGet + " /api/usage/history":          true,
	nethttp.MethodGet + " /api/usage/logs":             true,
	nethttp.MethodGet + " /api/usage/request-logs":     true,
	nethttp.MethodGet + " /api/usage/providers":        true,
	nethttp.MethodPost + " /api/locale":                true,
}

// roleAllows is the second gate: the class decides whether a request is
// authenticated at all, the role decides what a signed-in caller may reach.
func roleAllows(role, method, path string) bool {
	if role != auth.RoleUser {
		return true
	}
	// Pages and static files carry no data of their own; the API behind them is
	// what the list below narrows.
	if !strings.HasPrefix(path, "/api/") {
		return true
	}
	if userRoutes[method+" "+path] {
		return true
	}
	// One key, read or switched on and off. Ownership is checked by the handler.
	switch method {
	case nethttp.MethodGet, nethttp.MethodPut:
		return oneSegmentAfter(path, "/api/keys/")
	}
	return false
}

// oneSegmentAfter reports whether path names exactly one segment under prefix.
func oneSegmentAfter(path, prefix string) bool {
	rest, ok := strings.CutPrefix(path, prefix)
	return ok && rest != "" && !strings.Contains(rest, "/")
}
