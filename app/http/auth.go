package http

import (
	"context"
	nethttp "net/http"
	"time"

	"rllmx/app/identity"
)

// Authenticator judges who is calling and which checks apply. The guard asks
// it and nothing else about logins and keys.
type Authenticator interface {
	Session(r *nethttp.Request) bool
	// APIKey judges the key the request presents.
	APIKey(r *nethttp.Request) identity.KeyResult
	// RequireLogin reports whether the dashboard demands a login.
	RequireLogin(ctx context.Context) bool
	// RequireAPIKey reports whether a local client API call needs a valid key.
	RequireAPIKey(ctx context.Context) bool
}

// keyLimiter is the optional part of an Authenticator that throttles callers who
// present refused API keys. A guard whose authenticator lacks it does not
// throttle.
type keyLimiter interface {
	KeyLocked(r *nethttp.Request) time.Duration
	KeyFailed(r *nethttp.Request)
}

// denyAll is the authenticator of a guard built without one: nobody is signed
// in, no key is valid, and both requirements are on.
type denyAll struct{}

func (denyAll) Session(*nethttp.Request) bool              { return false }
func (denyAll) APIKey(*nethttp.Request) identity.KeyResult { return identity.KeyResult{} }
func (denyAll) RequireLogin(context.Context) bool          { return true }
func (denyAll) RequireAPIKey(context.Context) bool         { return true }
