package controllers

import (
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/access/auth"
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
	// decoy is a hash of a password nobody holds, checked in place of a stored
	// hash when the name is not a user, so the answer takes as long as a wrong
	// password does.
	decoy string
}

// NewAuthController builds the handler the auth routes serve. trust supplies the
// client address, the local test and the cookie's Secure flag.
func NewAuthController(a *auth.Authenticator, trust apphttp.Trust) *AuthController {
	c := &AuthController{Auth: a, Trust: trust, Now: time.Now, slots: make(chan struct{}, max(runtime.NumCPU(), 1))}
	if secret, err := auth.NewUserPassword(); err == nil {
		c.decoy, _ = a.Pass.Hash(secret)
	}
	return c
}

// Login answers POST /api/auth/login. An empty username is the admin; any other
// name is a standard user, checked against that user's own password. Every
// attempt is counted before the password is compared, so parallel guesses
// cannot all see the old count.
func (c *AuthController) Login(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	var body struct {
		Username string `json:"username"`
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

	if name := strings.TrimSpace(body.Username); name != "" {
		return c.loginUser(ctx, r, name, body.Password, key)
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

// loginUser checks a standard user's password. An unknown name is counted like
// a wrong password, so names cannot be probed faster than secrets.
func (c *AuthController) loginUser(ctx contractshttp.Context, r *http.Request, name, password, limitKey string) contractshttp.Response {
	invalid := func() contractshttp.Response {
		c.Auth.Limiter.Fail(limitKey, c.Now())
		if wait := c.Auth.Limiter.Locked(limitKey, c.Now()); wait > 0 {
			return tooMany(ctx, wait)
		}
		return ctx.Response().Json(http.StatusUnauthorized, contractshttp.Json{"error": "Invalid password"})
	}
	if c.Auth.Users == nil {
		return invalid()
	}
	u, ok := c.Auth.Users.ByUsername(r.Context(), name)
	stored := u.Hash
	if !ok {
		// Spend the same check an existing name costs, so the time to answer does
		// not say which names exist.
		stored = c.decoy
	}
	pass, busy := c.compareStored(r, stored, password)
	if busy {
		return flat(ctx, statusLocked, "The server is busy; try again")
	}
	if !pass || !ok {
		return invalid()
	}
	c.Auth.Limiter.Succeed(limitKey)
	cookie, issued := c.Auth.IssueUser(r.Context(), u, auth.MethodPassword, c.Trust.SecureRequest(r))
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

	// The epoch is shared by every session, so only the admin may move it: a
	// standard user doing so would sign the admin out at will.
	if who, ok := c.Auth.SessionInfo(r); body.Everywhere && ok && who.Admin() {
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
	who, session := c.Auth.SessionInfo(r)
	// The sign-in page renders its buttons from this answer, so which single
	// sign-on type is offered and how it is labelled cannot wait for a session.
	out := contractshttp.Json{
		"authenticated":  session,
		"role":           who.Role,
		"user":           who.Name,
		"authMode":       st.AuthMode,
		"ssoType":        st.SSOType,
		"sso":            mode.SSO(),
		"password":       mode.Password,
		"oidc":           mode.OIDC,
		"saml":           mode.SAML,
		"oidcLoginLabel": st.OIDCLoginLabel,
		"samlLoginLabel": st.SAMLLoginLabel,
	}
	if who.Role == auth.RoleUser {
		if u, ok := c.Auth.Users.Get(r.Context(), who.UserID); ok && u.Provisioned {
			out["needsPassword"] = true
		}
	}
	if (session && who.Admin()) || (!session && apphttp.IsLocalRequest(r, c.Auth.TrustLoopback)) {
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
	rc := r.Context()
	who, signed := c.Auth.SessionInfo(r)
	user := signed && who.Role == auth.RoleUser
	stored := c.Auth.Store.Get(rc).Password
	provisioned := false
	if user {
		u, ok := c.Auth.Users.Get(rc, who.UserID)
		if !ok {
			return flat(ctx, http.StatusUnauthorized, "The account no longer exists")
		}
		stored, provisioned = u.Hash, u.Provisioned
	}
	// A row single sign-on provisioned holds a password nobody knows; the
	// session already proved who the caller is, so this first set needs no
	// current password. Every later change goes through the usual check.
	if !provisioned {
		ok, busy := c.compareStored(r, stored, body.CurrentPassword)
		if busy {
			return flat(ctx, statusLocked, "The server is busy; try again")
		}
		if !ok {
			c.Auth.Limiter.Fail(key, c.Now())
			return ctx.Response().Json(http.StatusUnauthorized, contractshttp.Json{"error": "Invalid password"})
		}
	}
	c.Auth.Limiter.Succeed(key)

	hash, err := c.Auth.Pass.Hash(body.NewPassword)
	if err != nil {
		return flat(ctx, http.StatusInternalServerError, "Could not store the password")
	}
	if user {
		u, _ := c.Auth.Users.Get(rc, who.UserID)
		u.Hash, u.Provisioned = hash, false
		if err := c.Auth.Users.Put(rc, u); err != nil {
			return flat(ctx, http.StatusInternalServerError, "Could not store the password")
		}
		cookie, issued := c.Auth.IssueUser(rc, u, who.Method, c.Trust.SecureRequest(r))
		if !issued {
			return flat(ctx, http.StatusInternalServerError, "Could not start a session")
		}
		ctx.Response().Cookie(cookie)
		return ctx.Response().Json(http.StatusOK, contractshttp.Json{"success": true})
	}

	db := c.Auth.Store.DB()
	if db == nil {
		return flat(ctx, http.StatusInternalServerError, "The store is not open")
	}
	if err := db.Settings().SetPasswordHash(rc, hash); err != nil {
		return flat(ctx, http.StatusInternalServerError, "Could not store the password")
	}
	if _, err := c.Auth.Store.BumpEpoch(rc); err != nil {
		return flat(ctx, http.StatusInternalServerError, "Could not end the other sessions")
	}

	cookie, issued := c.Auth.Issue(rc, auth.MethodPassword, "", "", c.Trust.SecureRequest(r))
	if !issued {
		return flat(ctx, http.StatusInternalServerError, "Could not start a session")
	}
	ctx.Response().Cookie(cookie)
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"success": true})
}

// compare runs one password check inside a slot against the admin password.
func (c *AuthController) compare(r *http.Request, password string) (ok, busy bool) {
	st := c.Auth.Store.Get(r.Context())
	return c.compareStored(r, st.Password, password)
}

// compareStored runs one password check inside a slot against a stored hash.
// busy reports that every slot stayed taken for comparisonWait, which is a 503
// rather than a queued guess.
func (c *AuthController) compareStored(_ *http.Request, stored, password string) (ok, busy bool) {
	timer := time.NewTimer(comparisonWait)
	defer timer.Stop()
	select {
	case c.slots <- struct{}{}:
		defer func() { <-c.slots }()
	case <-timer.C:
		return false, true
	}
	return c.Auth.Pass.Check(stored, password), false
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
