package controllers

import (
	"net/http"
	"runtime"
	"strconv"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/auth"
	apphttp "rllmx/app/http"
)

// comparisonSlots bounds how many password comparisons run at once. bcrypt at
// cost 10 spends real CPU, so a burst of guesses must not take the whole
// process; a comparison waits this long for a slot, then the attempt is refused.
const (
	comparisonWait = 2 * time.Second
	statusLocked   = http.StatusServiceUnavailable
)

// AuthController answers the dashboard login routes.
type AuthController struct {
	Auth  *auth.Authenticator
	Trust apphttp.Trust
	// Now is the clock the limiter reads; tests replace it.
	Now func() time.Time
	// Slots bounds the concurrent comparisons. nil means the default of one per
	// CPU, built on first use.
	slots chan struct{}
}

// NewAuthController builds the handler the auth routes serve. trust supplies the
// client address, the local test and the cookie's Secure flag.
func NewAuthController(a *auth.Authenticator, trust apphttp.Trust) *AuthController {
	return &AuthController{Auth: a, Trust: trust, Now: time.Now, slots: make(chan struct{}, max(runtime.NumCPU(), 1))}
}

// Login answers POST /api/auth/login. The order is the one PRD 2 fixes.
func (c *AuthController) Login(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	var body struct {
		Password string `json:"password"`
	}
	if err := ctx.Request().Bind(&body); err != nil {
		return flat(ctx, http.StatusBadRequest, "Invalid request body")
	}

	if !c.Auth.Mode(r.Context()).Password {
		return flat(ctx, http.StatusForbidden, "Password login is disabled. Use single sign-on.")
	}

	key := auth.Key(c.Trust.ClientAddr(r))
	if wait := c.Auth.Limiter.Locked(key, c.Now()); wait > 0 {
		return tooMany(ctx, wait)
	}

	local := apphttp.IsLocalRequest(r, c.Auth.TrustLoopback)
	st := c.Auth.Store.Get(r.Context())
	if !local && st.Password == "" && c.Auth.Pass.WellKnown(st.Password) {
		// A fresh install's default is public, so no session is issued: a remote
		// caller changes the password from the machine itself first.
		return ctx.Response().Json(http.StatusForbidden, contractshttp.Json{
			"success": false, "mustChangePassword": true,
			"error": "Change the default password from this machine before signing in remotely.",
		})
	}

	ok, busy := c.compare(r, body.Password)
	if busy {
		return flat(ctx, statusLocked, "The server is busy; try again")
	}
	if !ok {
		c.Auth.Limiter.Fail(key, c.Now())
		if wait := c.Auth.Limiter.Locked(key, c.Now()); wait > 0 {
			return tooMany(ctx, wait)
		}
		return ctx.Response().Json(http.StatusUnauthorized, contractshttp.Json{"error": "Invalid password"})
	}

	c.Auth.Limiter.Succeed(key)
	cookie, issued := c.Auth.Issue(r.Context(), auth.MethodPassword, "", "", c.Trust.SecureRequest(r))
	if !issued {
		return flat(ctx, http.StatusInternalServerError, "Could not start a session")
	}
	ctx.Response().Cookie(cookie)
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"success": true, "mustChangePassword": false})
}

// Logout answers POST /api/auth/logout. `{"everywhere":true}` with a session
// ends every session by bumping the epoch.
func (c *AuthController) Logout(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	var body struct {
		Everywhere bool `json:"everywhere"`
	}
	_ = ctx.Request().Bind(&body)

	if body.Everywhere && c.Auth.Session(r) {
		if _, err := c.Auth.Store.BumpEpoch(r.Context()); err != nil {
			return flat(ctx, http.StatusInternalServerError, "Could not end the sessions")
		}
	}
	ctx.Response().Cookie(auth.ClearedCookie())
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"success": true})
}

// Status answers GET /api/auth/status. The password facts go only to a local or
// signed-in caller.
func (c *AuthController) Status(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	mode := c.Auth.Mode(r.Context())
	st := c.Auth.Store.Get(r.Context())
	session := c.Auth.Session(r)
	out := contractshttp.Json{
		"authenticated": session,
		"authMode":      st.AuthMode,
		"ssoType":       st.SSOType,
		"sso":           mode.SSO(),
		"password":      mode.Password,
	}
	if session || apphttp.IsLocalRequest(r, c.Auth.TrustLoopback) {
		out["hasPassword"] = st.Password != ""
		out["defaultPassword"] = c.Auth.Pass.WellKnown(st.Password)
	}
	return ctx.Response().Json(http.StatusOK, out)
}

// RequireLogin answers GET /api/settings/require-login: the flag alone.
func (c *AuthController) RequireLogin(ctx contractshttp.Context) contractshttp.Response {
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{
		"requireLogin": c.Auth.RequireLogin(ctx.Request().Origin().Context()),
	})
}

// ChangePassword answers POST /api/auth/change-password. It judges the new
// password, verifies the current one under the limiter, stores the hash, ends
// every other session and re-issues this one.
func (c *AuthController) ChangePassword(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := ctx.Request().Bind(&body); err != nil {
		return flat(ctx, http.StatusBadRequest, "Invalid request body")
	}
	if err := auth.Judge(body.NewPassword); err != nil {
		return flat(ctx, http.StatusBadRequest, err.Error())
	}

	key := auth.Key(c.Trust.ClientAddr(r))
	if wait := c.Auth.Limiter.Locked(key, c.Now()); wait > 0 {
		return tooMany(ctx, wait)
	}
	ok, busy := c.compare(r, body.CurrentPassword)
	if busy {
		return flat(ctx, statusLocked, "The server is busy; try again")
	}
	if !ok {
		c.Auth.Limiter.Fail(key, c.Now())
		return ctx.Response().Json(http.StatusUnauthorized, contractshttp.Json{"error": "Invalid password"})
	}
	c.Auth.Limiter.Succeed(key)

	db := c.Auth.Store.DB()
	if db == nil {
		return flat(ctx, http.StatusInternalServerError, "The store is not open")
	}
	hash, err := c.Auth.Pass.Hash(body.NewPassword)
	if err != nil {
		return flat(ctx, http.StatusInternalServerError, "Could not store the password")
	}
	if err := db.Settings().SetPasswordHash(r.Context(), hash); err != nil {
		return flat(ctx, http.StatusInternalServerError, "Could not store the password")
	}
	if _, err := c.Auth.Store.BumpEpoch(r.Context()); err != nil {
		return flat(ctx, http.StatusInternalServerError, "Could not end the other sessions")
	}

	cookie, issued := c.Auth.Issue(r.Context(), auth.MethodPassword, "", "", c.Trust.SecureRequest(r))
	if !issued {
		return flat(ctx, http.StatusInternalServerError, "Could not start a session")
	}
	ctx.Response().Cookie(cookie)
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"success": true})
}

// compare runs one password check inside a slot. busy reports that every slot
// stayed taken for comparisonWait, which is a 503 rather than a queued guess.
func (c *AuthController) compare(r *http.Request, password string) (ok, busy bool) {
	timer := time.NewTimer(comparisonWait)
	defer timer.Stop()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-timer.C:
		return false, true
	}
	st := c.Auth.Store.Get(r.Context())
	return c.Auth.Pass.Check(st.Password, password), false
}

func tooMany(ctx contractshttp.Context, wait time.Duration) contractshttp.Response {
	secs := max(int64((wait+time.Second-1)/time.Second), 1)
	ctx.Response().Header("Retry-After", strconv.FormatInt(secs, 10))
	return ctx.Response().Json(http.StatusTooManyRequests, contractshttp.Json{
		"error": "Too many failed attempts", "retryAfter": secs,
	})
}

// flat answers a dashboard route error in the flat shape.
func flat(ctx contractshttp.Context, status int, msg string) contractshttp.Response {
	return ctx.Response().Json(status, contractshttp.Json{"error": msg})
}
