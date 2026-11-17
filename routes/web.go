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
	// Media serves the non-chat endpoints; nil leaves their routes out.
	Media *controllers.MediaController
	// OAuth is the account login handler; nil leaves its routes out.
	OAuth *controllers.OAuthController
	// Callback serves the page a provider redirects the browser to.
	Callback *controllers.CallbackController
	// Keys is the client-key management handler; nil leaves its routes out.
	Keys *controllers.KeysController
	// Health carries the health feed; nil leaves the liveness route alone.
	Health *controllers.HealthController
	// Combos suggests and accepts a combo; nil leaves those routes out.
	Combos *controllers.CombosController
	// Settings reads and writes the dashboard's settings; nil leaves those
	// routes out.
	Settings *controllers.SettingsController
	// Logs serves the Console Log ring; nil leaves its routes out.
	Logs *controllers.LogsController
}

// Web declares the route tables. Every route goes through guard.Handle, which
// records its class; a route added on router directly is not in the guard's
// table, and the server refuses to start with one.
func Web(router route.Route, guard *apphttp.Guard, base Base) {
	health := controllers.NewHealthController()
	if base.Health != nil {
		health = base.Health
	}
	guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/api/health", health.Index)
	guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/health/latency-stream", health.LatencyStream)

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

	// A key is a secret, so its routes need a dashboard session even when
	// requireLogin is off: the class the account logins use.
	if k := base.Keys; k != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/keys", k.List)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/keys", k.Create)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/keys/{id}", k.Show)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPut, "/api/keys/{id}", k.Update)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/keys/{id}", k.Delete)
	}

	if p := base.Providers; p != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers", p.List)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers", p.Create)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/validate", p.Validate)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/test-batch", p.TestBatch)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/bulk", p.BulkCreate)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/{id}", p.Show)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPut, "/api/providers/{id}", p.Update)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/providers/{id}", p.Delete)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/{id}/test", p.Test)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/{id}/models", p.ModelsList)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/{id}/test-models", p.TestModels)
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

	if m := base.Media; m != nil {
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/embeddings", m.Embeddings)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/images/generations", m.Images)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/audio/speech", m.Speech)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/audio/transcriptions", m.Transcriptions)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/audio/voices", m.Voices)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/videos/generations", m.VideoGenerations)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/videos/edits", m.VideoEdits)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/videos/extensions", m.VideoExtensions)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/videos/{id}", m.VideoJob)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/search", m.Search)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/web/fetch", m.WebFetch)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/media-providers/tts/voices", m.TTSVoices)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/media-providers/tts/{provider}/voices", m.ProviderVoices)
	}

	if m := base.Models; m != nil {
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/models", m.List)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/models/*path", m.Show)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1beta/models", m.Gemini)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/models/test", m.Test)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/api/tags", m.Tags)
	}

	if cb := base.Combos; cb != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/suggest", cb.Suggest)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos/suggest/accept", cb.Accept)
	}

	if o := base.OAuth; o != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/{provider}/authorize", o.Authorize)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/{provider}/authorize", o.AuthorizeApp)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/{provider}/pat", o.ConnectToken)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/{provider}/import", o.Import)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/{provider}/bulk-import", o.BulkImport)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/{provider}/device-code", o.DeviceCode)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/poll", o.Poll)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/exchange", o.Exchange)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/oauth/callback", o.Callback)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/oauth/status", o.Status)
	}

	if s := base.Settings; s != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/settings", s.Show)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPatch, "/api/settings", s.Update)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodPost, "/api/locale", s.Locale)
	}

	if l := base.Logs; l != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/translator/console-logs", l.List)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/translator/console-logs", l.Clear)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/translator/console-logs/stream", l.Stream)
	}

	if cb := base.Callback; cb != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/callback", cb.Page)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/static/js/callback.js", cb.Script)
	}

	router.Fallback(guard.Fallback(router.GetRoutes))
}
