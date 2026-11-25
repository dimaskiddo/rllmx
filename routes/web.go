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
	// ClientUsage answers a client tool's "how much have I used" from the key's
	// own budget; nil leaves its routes out.
	ClientUsage *controllers.ClientUsageController
	// OAuth is the account login handler; nil leaves its routes out.
	OAuth *controllers.OAuthController
	// Pages serves the callback page and the static files; nil leaves them out.
	Pages *controllers.PagesController
	// Keys is the client-key management handler; nil leaves its routes out.
	Keys *controllers.KeysController
	// Users manages the dashboard's standard users; nil leaves its routes out.
	Users *controllers.UsersController
	// Health carries the health feed; nil leaves the liveness route alone.
	Health *controllers.HealthController
	// Combos suggests and accepts a combo; nil leaves those routes out.
	Combos *controllers.CombosController
	// Settings reads and writes the dashboard's settings; nil leaves those
	// routes out.
	Settings *controllers.SettingsController
	// Pricing reads and writes the rate overrides; nil leaves those routes out.
	Pricing *controllers.PricingController
	// Logs serves the Console Log ring; nil leaves its routes out.
	Logs *controllers.LogsController
	// Usage serves the usage pages and their feed; nil leaves those routes out.
	Usage *controllers.UsageController
	// Headroom serves the sidecar's dashboard routes; nil leaves them out.
	Headroom *controllers.HeadroomController
	// Skills serves the installed skills and the catalog sync; nil leaves them
	// out.
	Skills *controllers.SkillsController
	// About answers the update check the About page asks for; nil leaves its
	// route out.
	About *controllers.AboutController
	// Metrics serves the OpenMetrics scrape; nil leaves its route out.
	Metrics *controllers.MetricsController
}

// Web declares the route tables. Every route goes through guard.Handle, which
// records its class; a route added on router directly is not in the guard's
// table, and the server refuses to start with one.
func Web(router route.Route, guard *apphttp.Guard, base Base) {
	webBase(router, guard, base)
	webAccess(router, guard, base)
	webProviders(router, guard, base)
	webClient(router, guard, base)
	webDashboard(router, guard, base)
	webLogin(router, guard, base)
	router.Fallback(guard.Fallback(router.GetRoutes))
}

// webBase declares the routes every build has: health, version and setup.
func webBase(router route.Route, guard *apphttp.Guard, base Base) {
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

	// The scrape carries its own credential, so the session guard stays out of
	// its way; the handler refuses an unset one.
	if m := base.Metrics; m != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/metrics", m.Index)
	}
}

// webAccess declares the sign-in routes and the routes that manage keys.
func webAccess(router route.Route, guard *apphttp.Guard, base Base) {
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
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/keys/{id}/reveal", k.Reveal)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPut, "/api/keys/{id}", k.Update)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/keys/{id}", k.Delete)
	}
	if u := base.Users; u != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/users", u.List)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/users", u.Create)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPut, "/api/users/{id}", u.Update)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/users/{id}", u.Delete)
	}
}

// webProviders declares the provider and node management routes.
func webProviders(router route.Route, guard *apphttp.Guard, base Base) {
	if p := base.Providers; p != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers", p.List)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers", p.Create)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/validate", p.Validate)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/test-batch", p.TestBatch)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/providers/bulk", p.BulkCreate)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/client", p.Client)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/catalog", p.Catalog)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/suggested-models", p.SuggestedModels)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/providers/kilo/free-models", p.KiloFreeModels)
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
}

// webClient declares the routes an API client calls: chat, media and the model lists.
func webClient(router route.Route, guard *apphttp.Guard, base Base) {
	if c := base.Chat; c != nil {
		for _, path := range []string{
			"/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/responses/compact",
			"/v1beta/models/*path", "/v1/api/chat",
		} {
			guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, path, c.Serve)
		}
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodPost, "/v1/messages/count_tokens", c.CountTokens)
	}

	if u := base.ClientUsage; u != nil {
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/api/oauth/usage", u.Claude)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/organizations/{org}/usage", u.Claude)
		guard.Handle(router, apphttp.ClassLLM, nethttp.MethodGet, "/v1/usage", u.OpenCode)
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
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models", m.AdminList)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models/alias", m.Aliases)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPut, "/api/models/alias", m.SetAlias)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/models/alias", m.DeleteAlias)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models/custom", m.CustomModels)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/models/custom", m.AddCustomModel)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/models/custom", m.DeleteCustomModel)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models/disabled", m.DisabledModels)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/models/disabled", m.DisableModels)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/models/disabled", m.EnableModels)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models/availability", m.Availability)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/models/availability", m.ClearCooldown)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/models/catalog-sync", m.CatalogStatus)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/models/catalog-sync", m.CatalogRun)
	}
}

// webDashboard declares the dashboard's own reads and settings.
func webDashboard(router route.Route, guard *apphttp.Guard, base Base) {
	if a := base.About; a != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/about/update", a.Update)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/about/upgrade", a.RunUpgrade)
	}

	if cb := base.Combos; cb != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos", cb.List)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos", cb.Create)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/suggest", cb.Suggest)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos/suggest/accept", cb.Accept)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/presets", cb.Presets)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos/presets", cb.ApplyPresets)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/{id}", cb.Show)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPut, "/api/combos/{id}", cb.Update)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/combos/{id}", cb.Delete)
	}

	if p := base.Pricing; p != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/pricing", p.Show)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPatch, "/api/pricing", p.Update)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/pricing", p.Reset)
	}

	if u := base.Usage; u != nil {
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/stats", u.Stats)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/chart", u.Chart)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/history", u.History)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/logs", u.Logs)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/request-logs", u.Logs)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/providers", u.Providers)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/active-providers", u.ActiveProviders)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/stream", u.Stream)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/{id}", u.Connection)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/usage/{id}/claude-reset", u.ClaudeReset)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodGet, "/api/usage/{id}/codex-reset-credits", u.CodexCredits)
		guard.Handle(router, apphttp.ClassProtected, nethttp.MethodPost, "/api/usage/{id}/codex-reset-credits", u.CodexResetCredits)
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

	if h := base.Headroom; h != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/headroom/status", h.Status)
		for _, m := range []string{nethttp.MethodGet, nethttp.MethodPost, nethttp.MethodPut, nethttp.MethodPatch, nethttp.MethodDelete} {
			guard.Handle(router, apphttp.ClassAlwaysProtected, m, "/api/headroom/proxy/*path", h.Proxy)
		}
	}

	if s := base.Skills; s != nil {
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/skills", s.List)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/skills", s.Create)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/skills/{id}", s.Get)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPut, "/api/skills/{id}", s.Update)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/skills/{id}", s.Delete)
		guard.Handle(router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/skills/sync", s.Sync)
	}
}

// webLogin declares the account login routes and the page a provider redirects to.
func webLogin(router route.Route, guard *apphttp.Guard, base Base) {
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

	if p := base.Pages; p != nil {
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/", p.Root)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/login", p.Login)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/callback", p.Callback)
		guard.Handle(router, apphttp.ClassPublic, nethttp.MethodGet, "/static/*file", p.Asset)
		guard.Handle(router, apphttp.ClassPage, nethttp.MethodGet, "/dashboard", p.Dashboard)
		guard.Handle(router, apphttp.ClassPage, nethttp.MethodGet, "/dashboard/*path", p.Dashboard)
	}
}
