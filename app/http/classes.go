// Package http is the HTTP surface: the guard that decides who may reach which
// route, the path rewrite in front of the router, and the helpers they share.
package http

import "strings"

// Class says who may reach a route. Every route has exactly one.
type Class int

const (
	// ClassPublic needs nothing: health, version, static entry pages.
	ClassPublic Class = iota + 1
	// ClassProtected needs a session, or no login when requireLogin is off and the caller is local.
	ClassProtected
	// ClassAlwaysProtected needs a session even when requireLogin is off.
	ClassAlwaysProtected
	// ClassLocalOnly needs a local caller and a session, even when requireLogin is off.
	ClassLocalOnly
	// ClassLLM is the client API: an API key, or a local caller.
	ClassLLM
	// ClassPage is a dashboard page: without a session it redirects to /login.
	ClassPage
)

func (c Class) valid() bool { return c >= ClassPublic && c <= ClassPage }

// IsClientAPI reports whether a path belongs to the client API, which gets CORS,
// the client body cap and no page headers. `/api/tags` is Ollama's model list,
// so it is a client route even though the reference puts it under the dashboard.
// `/api/oauth/usage` is the one a Claude client asks how much of its plan is
// used: it carries an API key, not a dashboard cookie.
func IsClientAPI(p string) bool {
	return p == "/v1" || strings.HasPrefix(p, "/v1/") || p == "/v1beta" ||
		strings.HasPrefix(p, "/v1beta/") || p == "/api/tags" || p == "/api/oauth/usage"
}

// IsDashboardAPI reports whether a path belongs to the dashboard JSON API, whose
// responses are never cached.
func IsDashboardAPI(p string) bool { return p == "/api" || strings.HasPrefix(p, "/api/") }

// IsLongDashboardCall reports a dashboard route whose work outlives the default
// write deadline: a feed, the tests that wait on providers, and the two calls
// that download (an update, a skill catalog). Each such
// handler bounds its own work instead.
func IsLongDashboardCall(p string) bool {
	switch p {
	case "/api/health/latency-stream", "/api/providers/test-batch", "/api/translator/console-logs/stream", "/api/usage/stream",
		"/api/about/upgrade", "/api/skills/sync":
		return true
	}
	return strings.HasSuffix(p, "/test-models")
}

func isDashboardPage(p string) bool { return p == "/dashboard" || strings.HasPrefix(p, "/dashboard/") }

// unroutedClass picks the class for a path that matched no route, so a mistyped
// or hidden path is answered the same way as a real one.
func unroutedClass(path string) Class {
	switch {
	case IsClientAPI(path):
		return ClassLLM
	case IsDashboardAPI(path):
		return ClassProtected
	case isDashboardPage(path):
		return ClassPage
	}
	return ClassPublic
}

type verdict int

const (
	// verdictDeny is the zero value: a class the switch does not handle is refused.
	verdictDeny verdict = iota
	verdictAllow
	verdictUnauthorized
	verdictForbidden
	verdictLogin
)

// decide is the whole access policy as one pure function and denies by default.
// keyOK means a valid key, or a local call while the key requirement is off.
func decide(class Class, local, session, anonymousOK, keyOK bool) verdict {
	switch class {
	case ClassPublic:
		return verdictAllow
	case ClassProtected:
		if session || anonymousOK {
			return verdictAllow
		}
		return verdictUnauthorized
	case ClassAlwaysProtected:
		if session {
			return verdictAllow
		}
		return verdictUnauthorized
	case ClassLocalOnly:
		switch {
		case !local:
			return verdictForbidden
		case session:
			return verdictAllow
		}
		return verdictUnauthorized
	case ClassLLM:
		if keyOK {
			return verdictAllow
		}
		return verdictUnauthorized
	case ClassPage:
		if session || anonymousOK {
			return verdictAllow
		}
		return verdictLogin
	}
	return verdictDeny
}
