package controllers

import (
	"errors"
	nethttp "net/http"
	"net/url"
	"strconv"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/auth"
	apphttp "rllmx/app/http"
	"rllmx/app/oauth"
	"rllmx/app/settings"
	"rllmx/app/sso"
)

// bindName keeps this binding apart from the sign-in cookies' own.
const bindName = "oauth_bind"

// OAuthController answers the account login routes. It never returns a token,
// a secret or a verifier.
type OAuthController struct {
	Service *oauth.Service
	Auth    *auth.Authenticator
	Server  *settings.Server
}

func (c *OAuthController) bind(r *nethttp.Request) string {
	cookie := auth.RequestCookie(r, auth.CookieName)
	secret := c.Auth.Secret()
	if cookie == "" || len(secret) == 0 {
		return ""
	}
	return sso.BindFor(secret, bindName, cookie)
}

// routerPort is the port the callback address names: the request's own when it
// comes from this machine (which already means a loopback Host), else the
// configured one.
func (c *OAuthController) routerPort(r *nethttp.Request, local bool) int {
	if local {
		if u, err := url.Parse("http://" + r.Host); err == nil {
			if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 && p < 65536 {
				return p
			}
		}
	}
	return c.Server.Port
}

// Authorize answers GET /api/oauth/{provider}/authorize.
func (c *OAuthController) Authorize(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	bind := c.bind(r)
	if bind == "" {
		return flat(ctx, nethttp.StatusUnauthorized, "Sign in to the dashboard first")
	}
	local := apphttp.IsLocalRequest(r, c.Auth.TrustLoopback)
	started, err := c.Service.Start(ctx.Request().Route("provider"), bind, local, c.routerPort(r, local))
	switch {
	case errors.Is(err, oauth.ErrUnsupported):
		return flat(ctx, nethttp.StatusNotFound, "This provider has no login")
	case errors.Is(err, oauth.ErrDisabled):
		return flat(ctx, nethttp.StatusBadRequest, "This login is switched off: its client id is not configured")
	case err != nil:
		return flat(ctx, nethttp.StatusServiceUnavailable, "The login could not be started")
	}
	return ctx.Response().Json(nethttp.StatusOK, started)
}

// DeviceCode answers GET /api/oauth/{provider}/device-code: it starts a device
// login and returns what the user needs to approve it. The device code stays on
// the server.
func (c *OAuthController) DeviceCode(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	bind := c.bind(r)
	if bind == "" {
		return flat(ctx, nethttp.StatusUnauthorized, "Sign in to the dashboard first")
	}
	started, err := c.Service.StartDevice(r.Context(), ctx.Request().Route("provider"), bind)
	switch {
	case errors.Is(err, oauth.ErrUnsupported):
		return flat(ctx, nethttp.StatusNotFound, "This provider has no device login")
	case errors.Is(err, oauth.ErrDisabled):
		return flat(ctx, nethttp.StatusBadRequest, "This login is switched off: its client id is not configured")
	case oauth.ClassOf(err) == oauth.ClassTransport:
		return flat(ctx, nethttp.StatusBadGateway, "The provider could not be reached")
	case err != nil:
		return flat(ctx, nethttp.StatusServiceUnavailable, "The login could not be started")
	}
	return ctx.Response().Json(nethttp.StatusOK, started)
}

type pollBody struct {
	State string `json:"state"`
}

// Poll answers POST /api/oauth/poll: one look at a device login. The router asks
// the provider no more often than the provider allows, whatever the browser does.
func (c *OAuthController) Poll(ctx contractshttp.Context) contractshttp.Response {
	var b pollBody
	if err := bindJSON(ctx, &b); err != nil || b.State == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	r := ctx.Request().Origin()
	return ctx.Response().Json(nethttp.StatusOK, c.Service.PollDevice(r.Context(), b.State, c.bind(r)))
}

type exchangeBody struct {
	State string `json:"state"`
	Input string `json:"input"`
}

// Exchange answers POST /api/oauth/exchange: the user pasted what the browser
// showed after the provider's consent page.
func (c *OAuthController) Exchange(ctx contractshttp.Context) contractshttp.Response {
	var b exchangeBody
	if err := bindJSON(ctx, &b); err != nil || b.State == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	in, err := oauth.ParseInput(b.Input)
	if err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "The pasted text holds no authorization code")
	}
	return c.complete(ctx, b.State, in)
}

type callbackBody struct {
	State string `json:"state"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

// Callback answers POST /api/oauth/callback: the callback page forwards what
// the provider sent.
func (c *OAuthController) Callback(ctx contractshttp.Context) contractshttp.Response {
	var b callbackBody
	if err := bindJSON(ctx, &b); err != nil || b.State == "" {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if b.Error != "" {
		return c.complete(ctx, b.State, oauth.Input{Failed: true})
	}
	in, err := oauth.ParseInput(b.Code)
	if err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "The callback holds no authorization code")
	}
	return c.complete(ctx, b.State, in)
}

func (c *OAuthController) complete(ctx contractshttp.Context, state string, in oauth.Input) contractshttp.Response {
	r := ctx.Request().Origin()
	id, err := c.Service.Complete(r.Context(), state, in, c.bind(r))
	var fail *oauth.Failure
	switch {
	case err == nil:
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true, "connectionId": id})
	case errors.Is(err, oauth.ErrSession):
		return flat(ctx, nethttp.StatusBadRequest, "The login was not found, has expired or was already used")
	case errors.As(err, &fail) && fail.Code == oauth.CodeStorage:
		return flat(ctx, nethttp.StatusServiceUnavailable, "Storage is not available")
	case errors.As(err, &fail):
		return ctx.Response().Json(nethttp.StatusBadRequest, contractshttp.Json{"error": "The login failed", "code": fail.Code})
	}
	return flat(ctx, nethttp.StatusInternalServerError, "The login failed")
}

// Status answers GET /api/oauth/status?state=: the outcome of a login, read
// once, for the browser that started it.
func (c *OAuthController) Status(ctx contractshttp.Context) contractshttp.Response {
	r := ctx.Request().Origin()
	return ctx.Response().Json(nethttp.StatusOK, c.Service.Sessions.Status(r.URL.Query().Get("state"), c.bind(r)))
}
