package routes

import (
	"context"
	nethttp "net/http"

	"github.com/goravel/framework/contracts/route"

	apphttp "rllmx/app/http"
	"rllmx/app/http/controllers"
)

// Base is what the two base routes report. Connections is read per request,
// because the store opens in a runner and routes are declared before any runner
// starts; nil means the count is unknown and no setup is needed.
type Base struct {
	Version     string
	Commit      string
	Connections func(ctx context.Context) (int64, error)
	// Auth is the dashboard login handler; nil leaves the auth routes out.
	Auth *controllers.AuthController
	// OIDC is the single sign-on handler; nil leaves its routes out.
	OIDC *controllers.OIDCController
	// SAML is the other single sign-on handler; nil leaves its routes out.
	SAML *controllers.SAMLController
	// Providers and Nodes are the provider management handlers; nil leaves their routes out.
	Providers *controllers.ProvidersController
	Nodes     *controllers.NodesController
	// Chat is the client chat handler; nil leaves its route out.
	Chat *controllers.ChatController
	// Models is the model catalog handler; nil leaves its routes out.
	Models *controllers.ModelsController
	// OAuth is the account login handler; nil leaves its routes out.
	OAuth *controllers.OAuthController
	// Callback serves the page a provider redirects the browser to.
	Callback *controllers.CallbackController
}

// Web declares the route tables. Every route goes through guard.Handle, which
// records its class; a route added on router directly is not in the guard's
// table, and the server refuses to start with one.
func Web(router route.Route, guard *apphttp.Guard, base Base) {
	health := controllers.NewHealthController()
	guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/health", health.Index)

	version := controllers.NewVersionController(base.Version, base.Commit)
	guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/version", version.Index)

	setup := controllers.NewInitController(base.Connections)
	guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/init", setup.Index)

	if a := base.Auth; a != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodPost, "/api/auth/login", a.Login)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodPost, "/api/auth/logout", a.Logout)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/status", a.Status)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/settings/require-login", a.RequireLogin)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/auth/change-password", a.ChangePassword)
	}

	if o := base.OIDC; o != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/oidc/start", o.Start)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/oidc/callback", o.Callback)
	}

	if m := base.SAML; m != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/saml/start", m.Start)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodPost, "/api/auth/saml/acs", m.ACS)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/auth/saml/metadata", m.Metadata)
	}

	if p := base.Providers; p != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers", p.List)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers", p.Create)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/validate", p.Validate)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/{id}", p.Show)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPut, "/api/providers/{id}", p.Update)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/providers/{id}", p.Delete)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/{id}/test", p.Test)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/{id}/models", p.ModelsList)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/{id}/overrides", p.Overrides)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPut, "/api/providers/{id}/overrides", p.SetOverrides)
	}

	if n := base.Nodes; n != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/provider-nodes", n.List)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/provider-nodes", n.Create)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/provider-nodes/validate", n.Validate)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPut, "/api/provider-nodes/{id}", n.Update)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/provider-nodes/{id}", n.Delete)
	}

	if c := base.Chat; c != nil {
		for _, path := range []string{
			"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/responses/compact",
			"/v1beta/models/*path", "/v1/api/chat",
		} {
			guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, path, c.Serve)
		}
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/messages/count_tokens", c.CountTokens)
	}

	if m := base.Models; m != nil {
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/models", m.List)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/models/*path", m.Show)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1beta/models", m.Gemini)
	}

	if o := base.OAuth; o != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/{provider}/authorize", o.Authorize)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/{provider}/authorize", o.AuthorizeApp)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/{provider}/pat", o.ConnectToken)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/{provider}/device-code", o.DeviceCode)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/poll", o.Poll)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/exchange", o.Exchange)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/callback", o.Callback)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/status", o.Status)
	}

	if cb := base.Callback; cb != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/callback", cb.Page)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/static/js/callback.js", cb.Script)
	}

	router.Fallback(guard.Fallback(router.GetRoutes))
}
