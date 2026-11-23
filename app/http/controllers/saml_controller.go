package controllers

import (
	"context"
	nethttp "net/http"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/access/auth"
	"rllmx/app/access/saml"
	"rllmx/app/access/sso"
	apphttp "rllmx/app/http"
	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
)

// samlACS is the path the identity provider posts to. It is the one route the
// cross-site guard exempts, in app/http/middleware.
const samlACS = "/api/auth/saml/acs"

// SAMLBindCookie ties a callback to the browser that started the sign-in. It is
// a second cookie, not a second meaning for the OIDC one, so two flows open in
// one browser cannot cross.
const SAMLBindCookie = "saml_bind"

// SAMLController answers the three SAML routes.
type SAMLController struct {
	Auth    *auth.Authenticator
	Trust   apphttp.Trust
	Server  *settings.Server
	Log     log.Log
	DB      func() *repository.Store
	Pending *sso.Pending
	// Replay remembers assertion ids already used.
	Replay *saml.Replay
}

// Start answers GET /api/auth/saml/start: it mints a request id, keeps it and
// sends the browser to the identity provider.
func (c *SAMLController) Start(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	base, ok := sso.PublicBase(c.Server, r, c.local(r))
	if !ok {
		return c.fail(ctx, "", sso.CodeNotConfigured)
	}
	set := c.settings(r.Context())
	if !set.Configured() || !c.Auth.Mode(r.Context()).SAML {
		return c.fail(ctx, base, sso.CodeDisabled)
	}

	key := ssoKey(c.Trust.ClientAddr(r))
	if wait := c.Auth.Limiter.Locked(key, c.Auth.Now()); wait > 0 {
		return c.fail(ctx, base, sso.CodeBusy)
	}
	secret := c.Auth.Secret()
	if len(secret) == 0 {
		return c.fail(ctx, base, sso.CodeIDPError)
	}

	entityID := set.EntityIDFor(base)
	acsURL := sso.CleanURL(base, samlACS)
	if _, err := saml.Metadata(entityID, acsURL); err != nil {
		return c.fail(ctx, base, sso.CodeNotConfigured)
	}

	id, err := saml.NewRequestID()
	if err != nil {
		return c.fail(ctx, base, sso.CodeIDPError)
	}
	target, err := saml.AuthnRequest(set.EntryPoint, entityID, acsURL, id, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return c.fail(ctx, base, sso.CodeNotConfigured)
	}
	// The flow state records what was in force when it started, so a settings
	// change mid-flow cannot redirect it to another provider.
	if err := c.Pending.PutKeyed(id, sso.State{Issuer: set.IDPEntityID, ClientID: entityID, Verifier: id, Owner: key}); err != nil {
		return c.fail(ctx, base, sso.CodeBusy)
	}

	ctx.Response().Cookie(contractshttp.Cookie{
		Name: SAMLBindCookie, Value: sso.BindFor(secret, SAMLBindCookie, id),
		Path: "/api/auth/saml", MaxAge: sso.BindTTL, HttpOnly: true,
		SameSite: samlSameSite(c.Auth.ForceTLS || c.Trust.SecureRequest(r)),
		Secure:   c.Auth.ForceTLS || c.Trust.SecureRequest(r),
	})
	return c.redirect(ctx, target)
}

// ACS answers POST /api/auth/saml/acs: the identity provider's cross-site POST.
func (c *SAMLController) ACS(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	base, ok := sso.PublicBase(c.Server, r, c.local(r))
	if !ok {
		return c.fail(ctx, "", sso.CodeNotConfigured)
	}
	key := ssoKey(c.Trust.ClientAddr(r))

	// Body only: a query parameter is never read, and a body over the cap is
	// refused before the parser sees it.
	r.Body = nethttp.MaxBytesReader(ctx.Response().Writer(), r.Body, saml.ACSBodyLimit)
	if err := r.ParseForm(); err != nil {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeInvalid)
	}
	if r.Form.Get("SAMLart") != "" {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeInvalid)
	}
	encoded := r.PostForm.Get("SAMLResponse")
	relay := r.PostForm.Get("RelayState")
	if encoded == "" || relay == "" {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeInvalid)
	}
	if !sso.BindMatchesFor(c.Auth.Secret(), SAMLBindCookie, relay, auth.RequestCookie(r, SAMLBindCookie)) {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeInvalid)
	}

	state, ok := c.Pending.Take(relay)
	if !ok {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeExpired)
	}

	set := c.settings(r.Context())
	entityID := set.EntityIDFor(base)
	if !set.Configured() || !c.Auth.Mode(r.Context()).SAML || set.IDPEntityID != state.Issuer || entityID != state.ClientID {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNotConfigured)
	}
	cert, err := saml.ParseCertificate(set.Cert)
	if err != nil {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNotConfigured)
	}

	assertion, err := saml.Verify(encoded, saml.Expect{
		Issuer:      set.IDPEntityID,
		Audience:    entityID,
		Destination: sso.CleanURL(base, samlACS),
		RequestID:   state.Verifier,
		Cert:        cert,
		Replay:      c.Replay,
		Now:         c.Auth.Now(),
	})
	if err != nil {
		c.countFailure(key)
		sso.Debugf(c.Log, "saml response refused: %v", err)
		return c.fail(ctx, base, sso.CodeInvalid)
	}
	sso.Debugf(c.Log, "saml sign-in for issuer %s assertion %s", sso.ShortHash(set.IDPEntityID), sso.ShortHash(assertion.ID))

	email, exactOnly, ok := saml.Email(assertion, set)
	if !ok {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNoEmail)
	}

	cookie, issued := c.Auth.IssueSSO(r.Context(), auth.MethodSAML, email, saml.Name(assertion, set), exactOnly, c.Trust.SecureRequest(r))
	if !issued {
		c.countFailure(key)
		return c.fail(ctx, base, sso.CodeNoAccount)
	}
	c.Auth.Limiter.Succeed(key)
	ctx.Response().Cookie(cookie)
	ctx.Response().Cookie(contractshttp.Cookie{Name: SAMLBindCookie, Value: "", Path: "/api/auth/saml", MaxAge: -1, HttpOnly: true})
	return c.redirect(ctx, sso.CleanURL(base, "/"))
}

// Metadata answers GET /api/auth/saml/metadata, the document an identity
// provider is configured from. It is served only while SAML is in force.
func (c *SAMLController) Metadata(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	base, ok := sso.PublicBase(c.Server, r, c.local(r))
	if !ok {
		return c.fail(ctx, "", sso.CodeNotConfigured)
	}
	set := c.settings(r.Context())
	if !set.Configured() || !c.Auth.Mode(r.Context()).SAML {
		return flat(ctx, nethttp.StatusNotFound, "Not found")
	}
	entityID := set.EntityIDFor(base)
	raw, err := saml.Metadata(entityID, sso.CleanURL(base, samlACS))
	if err != nil {
		return c.fail(ctx, base, sso.CodeNotConfigured)
	}
	ctx.Response().Header("Content-Type", "application/xml")
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().String(nethttp.StatusOK, string(raw))
}

// settings reads the SAML configuration from the settings row.
func (c *SAMLController) settings(ctx context.Context) saml.Settings {
	db := c.DB()
	if db == nil {
		return saml.Settings{}
	}
	d, err := db.Settings().Effective(ctx)
	if err != nil {
		return saml.Settings{}
	}
	return saml.FromDoc(d)
}

func (c *SAMLController) local(r *nethttp.Request) bool {
	return apphttp.IsLocalRequest(r, c.Auth.TrustLoopback)
}

// countFailure records one failed sign-in under the provider prefix, so it never
// spends a password attempt's budget.
func (c *SAMLController) countFailure(key string) { c.Auth.Limiter.Fail(key, c.Auth.Now()) }

// fail redirects to the login page with a fixed code and clears the bind cookie.
func (c *SAMLController) fail(ctx contractshttp.Context, base, code string) contractshttp.Response {
	ctx.Response().Cookie(contractshttp.Cookie{Name: SAMLBindCookie, Value: "", Path: "/api/auth/saml", MaxAge: -1, HttpOnly: true})
	if base == "" {
		return ctx.Response().Json(nethttp.StatusBadRequest, contractshttp.Json{"error": code})
	}
	return c.redirect(ctx, sso.LoginURL(base, code))
}

// redirect answers a 302 with no body.
func (c *SAMLController) redirect(ctx contractshttp.Context, target string) contractshttp.Response {
	ctx.Response().Header("Location", target)
	ctx.Response().Header("Cache-Control", "no-store")
	ctx.Request().Abort(nethttp.StatusFound)
	return nil
}

// samlSameSite is the one place the ACS's cross-site cookie differs: the POST
// arrives from the identity provider's origin, so the cookie must be `None`, and
// a browser rejects `None` without `Secure`. Plain HTTP therefore gets `Lax`,
// which still works for a same-site deployment.
func samlSameSite(secure bool) string {
	if secure {
		return "none"
	}
	return "lax"
}
