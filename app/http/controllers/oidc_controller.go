package controllers

import (
	"context"
	nethttp "net/http"
	"net/url"
	"strings"

	"github.com/go-jose/go-jose/v4"
	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/access/auth"
	"rllmx/app/access/oidc"
	"rllmx/app/access/sso"
	apphttp "rllmx/app/http"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
)

// OIDCController answers the two single sign-on routes. Every failure is a
// redirect to the login page with a fixed code, because the caller is a browser
// mid-flow, never a script reading JSON.
type OIDCController struct {
	Auth   *auth.Authenticator
	Trust  apphttp.Trust
	Server *settings.Server
	Client *sso.Client
	Cache  *cache.Cache
	Log    log.Log
	// DB reads the settings row, which carries the provider configuration.
	DB func() *repository.Store
	// Pending is the single-use store a sign-in lives in. One per process, so a
	// flow must finish on the instance that started it.
	Pending *sso.Pending
}

// Start answers GET /api/auth/oidc/start: it builds the authorization request
// and sends the browser to the provider.
func (c *OIDCController) Start(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	base, ok := sso.PublicBase(c.Server, r, c.local(r))
	if !ok {
		return c.fail(ctx, "", sso.CodeNotConfigured)
	}

	set := c.settings(r.Context())
	if !set.Configured() || !c.Auth.Mode(r.Context()).OIDC {
		return c.fail(ctx, base, sso.CodeDisabled)
	}

	key := ssoKey(c.Trust.ClientAddr(r))
	if wait := c.Auth.Limiter.Locked(key, c.Auth.Now()); wait > 0 {
		return c.fail(ctx, base, sso.CodeBusy)
	}
	if len(c.Auth.Secret()) == 0 {
		return c.fail(ctx, base, sso.CodeIDPError)
	}

	doc, err := oidc.Discover(r.Context(), c.Client, c.Cache, set.Issuer)
	if err != nil {
		sso.Debugf(c.Log, "oidc discovery failed: %v", err)
		return c.fail(ctx, base, sso.CodeIDPError)
	}

	verifier, err := oidc.Verifier()
	if err != nil {
		return c.fail(ctx, base, sso.CodeIDPError)
	}
	nonce, err := sso.Nonce()
	if err != nil {
		return c.fail(ctx, base, sso.CodeIDPError)
	}

	state, err := c.Pending.Put(sso.State{
		Nonce: nonce, Verifier: verifier, Issuer: doc.Issuer, ClientID: set.ClientID, Owner: key,
	})
	if err != nil {
		return c.fail(ctx, base, sso.CodeBusy)
	}

	ctx.Response().Cookie(contractshttp.Cookie{
		Name: sso.BindCookie, Value: sso.Bind(c.Auth.Secret(), state),
		Path: "/api/auth/oidc", MaxAge: sso.BindTTL, HttpOnly: true, SameSite: "lax",
		Secure: c.Auth.ForceTLS || c.Trust.SecureRequest(r),
	})

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", set.ClientID)
	q.Set("redirect_uri", sso.CleanURL(base, "/api/auth/oidc/callback"))
	q.Set("scope", oidc.SortedScopes(set.Scopes))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", oidc.Challenge(verifier))
	q.Set("code_challenge_method", oidc.Method)

	target := doc.AuthorizationEndpoint
	if strings.Contains(target, "?") {
		target += "&" + q.Encode()
	} else {
		target += "?" + q.Encode()
	}
	return c.redirect(ctx, target)
}

// Callback answers GET /api/auth/oidc/callback: it takes the sign-in, verifies
// the token and issues the session.
func (c *OIDCController) Callback(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	base, ok := sso.PublicBase(c.Server, r, c.local(r))
	if !ok {
		return c.fail(ctx, "", sso.CodeNotConfigured)
	}
	key := ssoKey(c.Trust.ClientAddr(r))

	// A provider that reports its own failure is passed through as one code.
	if r.URL.Query().Get("error") != "" {
		sso.Debugf(c.Log, "oidc provider reported an error: %s", r.URL.Query().Get("error"))
		return c.fail(ctx, base, sso.CodeIDPError)
	}
	code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
	if code == "" || state == "" {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeInvalid)
	}
	if !sso.BindMatches(c.Auth.Secret(), state, auth.RequestCookie(r, sso.BindCookie)) {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeInvalid)
	}

	pending, ok := c.Pending.Take(state)
	if !ok {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeExpired)
	}

	set := c.settings(r.Context())
	// Discovery stores the canonical issuer, so the settings row is canonicalised
	// here too. A row written elsewhere (an imported database, an older build) may
	// keep a trailing slash or a capitalised host, and that is the same provider,
	// not a settings change mid-flow.
	issuer, issuerErr := oidc.ParseIssuer(set.Issuer)
	mode := c.Auth.Mode(r.Context())
	if issuerErr != nil || !set.Configured() || !mode.OIDC || issuer != pending.Issuer || set.ClientID != pending.ClientID {
		sso.Debugf(c.Log, "oidc callback settings no longer match the flow: issuer=%t configured=%t usable=%t issuerMatch=%t clientMatch=%t",
			issuerErr == nil, set.Configured(), mode.OIDC, issuer == pending.Issuer, set.ClientID == pending.ClientID)
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNotConfigured)
	}

	doc, err := oidc.Discover(r.Context(), c.Client, c.Cache, set.Issuer)
	if err != nil {
		c.countFailure(key)
		sso.Debugf(c.Log, "oidc discovery failed on the callback: %v", err)
		return c.fail(ctx, base, sso.CodeIDPError)
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", sso.CleanURL(base, "/api/auth/oidc/callback"))
	form.Set("client_id", set.ClientID)
	form.Set("client_secret", set.ClientSecret)
	form.Set("code_verifier", pending.Verifier)

	var tokens struct {
		IDToken     string `json:"id_token"`
		AccessToken string `json:"access_token"`
	}
	if err := c.Client.PostForm(r.Context(), doc.TokenEndpoint, form, &tokens); err != nil {
		c.countFailure(key)
		sso.Debugf(c.Log, "oidc token exchange failed: %v", err)
		return c.fail(ctx, base, sso.CodeIDPError)
	}

	set2, err := c.jwks(r.Context(), doc)
	if err != nil {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeIDPError)
	}
	claims, err := oidc.VerifyIDToken(tokens.IDToken, set2, doc.Issuer, oidc.Expect{
		ClientID: set.ClientID, Nonce: pending.Nonce, AccessToken: tokens.AccessToken, Now: c.Auth.Now(),
	})
	if err != nil {
		c.countFailure(key)
		sso.Debugf(c.Log, "oidc id token refused: %v", err)
		if err == oidc.ErrUnverified {
			return c.fail(ctx, base, sso.CodeUnverified)
		}
		return c.fail(ctx, base, sso.CodeInvalid)
	}
	sso.Debugf(c.Log, "oidc sign-in for issuer %s subject %s", sso.ShortHash(claims.Issuer), sso.ShortHash(claims.Subject))

	email, exactOnly, ok := oidc.Email(claims)
	if !ok {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNoEmail)
	}

	cookie, issued := c.Auth.IssueSSO(r.Context(), auth.MethodOIDC, email, oidc.DisplayName(claims), exactOnly, c.Trust.SecureRequest(r))
	if !issued {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNoAccount)
	}
	c.Auth.Limiter.Succeed(key)
	ctx.Response().Cookie(cookie)
	ctx.Response().Cookie(contractshttp.Cookie{Name: sso.BindCookie, Value: "", Path: "/api/auth/oidc", MaxAge: -1, HttpOnly: true, SameSite: "lax"})
	return c.redirect(ctx, sso.CleanURL(base, "/"))
}

// settings reads the provider configuration from the settings row.
func (c *OIDCController) settings(ctx context.Context) oidc.Settings {
	db := c.DB()
	if db == nil {
		return oidc.Settings{}
	}
	d, err := db.Settings().Effective(ctx)
	if err != nil {
		return oidc.Settings{}
	}
	return oidc.FromDoc(d)
}

// jwks fetches the provider's published keys.
func (c *OIDCController) jwks(ctx context.Context, doc oidc.Document) (*jose.JSONWebKeySet, error) {
	return oidc.Keys(ctx, c.Client, c.Cache, doc.JWKSURI)
}

// local reports whether the request came from this machine.
func (c *OIDCController) local(r *nethttp.Request) bool {
	return apphttp.IsLocalRequest(r, c.Auth.TrustLoopback)
}

// countFailure records one failed callback, under its own prefix so a provider
// failure never spends a password attempt's budget.
func (c *OIDCController) countFailure(key string) {
	c.Auth.Limiter.Fail(key, c.Auth.Now())
}

// fail redirects to the login page with a fixed code and clears the bind cookie.
func (c *OIDCController) fail(ctx contractshttp.Context, base, code string) contractshttp.Response {
	ctx.Response().Cookie(contractshttp.Cookie{Name: sso.BindCookie, Value: "", Path: "/api/auth/oidc", MaxAge: -1, HttpOnly: true, SameSite: "lax"})
	if base == "" {
		// Without a base there is nowhere to send the browser but the same host,
		// so answer the code directly rather than build a URL from its Host.
		return ctx.Response().Json(nethttp.StatusBadRequest, contractshttp.Json{"error": code})
	}
	return c.redirect(ctx, sso.LoginURL(base, code))
}

// redirect answers a 302, with no body for HEAD.
func (c *OIDCController) redirect(ctx contractshttp.Context, target string) contractshttp.Response {
	ctx.Response().Header("Location", target)
	ctx.Response().Header("Cache-Control", "no-store")
	ctx.Request().Abort(nethttp.StatusFound)
	return nil
}

// ssoKey prefixes a limiter key, so a provider failure never shares a count with
// a password attempt.
func ssoKey(addr string) string { return "sso:" + auth.Key(addr) }
