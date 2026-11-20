package bootstrap

import (
	"context"
	"errors"
	nethttp "net/http"
	"path/filepath"

	"github.com/goravel/framework/contracts/console"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	contractsfoundation "github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/foundation/configuration"
	"github.com/goravel/framework/contracts/schedule"
	"github.com/goravel/framework/foundation"

	"rllmx/app/access/identity"
	"rllmx/app/access/saml"
	"rllmx/app/access/sso"
	"rllmx/app/facades"
	"rllmx/app/gateway/skill"
	"rllmx/app/http/controllers"
	"rllmx/app/http/middleware"
	"rllmx/app/platform/network"
	"rllmx/app/providers"
	"rllmx/app/store/data"
	"rllmx/app/store/repository"
	"rllmx/config"
	"rllmx/routes"
	"rllmx/web"
)

// Boot builds the application. Artisan commands run inside Create, before Boot
// returns, so anything they must show is registered in the callback.
func Boot(version, commit string) contractsfoundation.Application {
	return foundation.Setup().
		WithCommands(func() []console.Command { return Commands(version, commit) }).
		WithMiddleware(func(m configuration.Middleware) {
			h := providers.HTTPOf(facades.App())
			middleware.Stack(m, middleware.Options{
				Guard:   h.Guard,
				Trust:   h.Trust,
				Log:     facades.Log(),
				MaxBody: int64(h.Settings.MaxBodyMB) << 20,
			})
		}).
		WithRouting(func() { declareRoutes(version, commit) }).
		WithProviders(Providers(version)).
		WithMigrations(func() []contractsschema.Migration {
			return repository.Migrations(func() contractsschema.Schema { return facades.Schema() }, data.DialectOf(facades.Config()))
		}).
		WithSchedule(func() []schedule.Event { return providers.ScheduleOf(facades.App()) }).
		WithCommandsFilter(CommandsAllowlist).
		WithConfig(config.Boot).
		WithCallback(func() {
			facades.App().About("Router-LLM", []contractsfoundation.AboutItem{
				{Key: "Version", Value: version},
				{Key: "Commit", Value: commit},
			})
		}).
		Create()
}

// declareRoutes builds every controller from the bound pieces and declares the
// route tables. The store and the pipeline open later, in runners, so each
// controller takes them through a closure.
func declareRoutes(version, commit string) {
	h := providers.HTTPOf(facades.App())
	db := func() *repository.Store { return providers.DataOf(facades.App()).Runner.Store() }
	base := routeBase(version, commit)
	base.Auth = controllers.NewAuthController(h.Auth, h.Trust)
	base.OIDC = oidcController(h)
	base.SAML = samlController(h)
	base.Providers = &controllers.ProvidersController{Registry: h.Registry, DB: db, Checker: h.Checker, Refresher: h.Refresher, Enqueue: h.Probes.Enqueue, Probe: h.Probes.Prober}
	base.Nodes = &controllers.NodesController{Registry: h.Registry, DB: db, Checker: h.Checker, Changed: h.Snapshot.Invalidate}
	base.OAuth = &controllers.OAuthController{Service: h.OAuth, Auth: h.Auth, Server: h.Settings}
	base.Callback = &controllers.CallbackController{Files: web.Static}
	base.Health = &controllers.HealthController{DB: db, Tracker: h.Health}
	base.Settings = &controllers.SettingsController{DB: db, Auth: h.Auth, Secure: h.Trust.SecureRequest}
	base.Logs = &controllers.LogsController{Book: config.ConsoleLog()}
	base.Keys = keysController(h, db)
	clientRoutes(&base, h, db)
	dashboardRoutes(&base, h, db)
	routes.Web(facades.Route(), h.Guard, base)
}

// clientRoutes fills the controllers an API client calls: chat, media and the
// model lists.
func clientRoutes(base *routes.Base, h *providers.HTTP, db func() *repository.Store) {
	media := providers.NewMediaSource(facades.App())
	base.Chat = &controllers.ChatController{Pipe: h.Pipe.Get, Log: facades.Log()}
	base.Media = &controllers.MediaController{
		EmbeddingsPipe: media.Get, ImagesPipe: media.Get, SpeechPipe: media.Get,
		TranscribePipe: media.Get, VoicesPipe: media.Get, VideosPipe: media.Get,
		SearchPipe: media.Get, FetchPipe: media.Get, Log: facades.Log(),
	}
	base.Models = &controllers.ModelsController{
		Catalog: providers.NewCatalogSource(facades.App()).Get, Probe: h.Probes.Prober, Log: facades.Log(),
		Registry: h.Registry, Caps: h.Capabilities, DB: db, Changed: h.Snapshot.Invalidate,
		CatalogSync: func() controllers.CatalogSyncer {
			if h.CatalogSync == nil {
				return nil
			}
			return h.CatalogSync
		},
	}
}

// dashboardRoutes fills the controllers behind the dashboard's own pages.
func dashboardRoutes(base *routes.Base, h *providers.HTTP, db func() *repository.Store) {
	base.Combos = &controllers.CombosController{
		DB: db, Probe: h.Probes.Prober, Tracker: h.Health, Prices: h.Prices.Get, Log: facades.Log(),
		Registry: h.Registry, Changed: h.Snapshot.Invalidate, Reset: h.ResetCombo,
	}
	base.Pricing = &controllers.PricingController{
		DB: db, Table: h.Prices.Get, Log: facades.Log(),
		Reload: func() {
			h.Prices.Reset()
			if _, err := h.Prices.Get(); err != nil {
				facades.Log().Warningf("pricing: the table did not reload: %v", err)
			}
		},
	}
	base.Usage = &controllers.UsageController{
		Analytics: func() controllers.UsageReader {
			if s := providers.DataOf(facades.App()).Runner.Analytics(); s != nil {
				return s
			}
			return nil
		},
		Flushed: func() uint64 {
			if w := providers.DataOf(facades.App()).Runner.Writer(); w != nil {
				return w.Flushed()
			}
			return 0
		},
		DB: db, Registry: h.Registry, Inflight: h.Inflight, Log: facades.Log(),
		Quota: func() controllers.QuotaReader {
			if h.Quota == nil {
				return nil
			}
			return h.Quota
		},
	}
	base.Headroom = &controllers.HeadroomController{
		DB: db, Client: headroomClient(h), DefaultURL: headroomURL(h),
	}
	// The pipeline builds its registry from the same resolved folder, so a
	// dashboard sync and a request see the same skills.
	dir := providers.DataOf(facades.App()).Settings.DataDir
	base.Skills = &controllers.SkillsController{
		DB: db, Dir: dir, Reg: skill.NewRegistry(filepath.Join(dir, "skills"), skill.Builtin()), Log: facades.Log(),
	}
}

// headroomClient builds the guarded client for one sidecar host; the address is
// the operator's setting, so that host alone is added to the outbound allow list.
func headroomClient(h *providers.HTTP) func(string) *nethttp.Client {
	return func(host string) *nethttp.Client {
		return network.NewClient(network.Options{
			ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: -1,
			Allow: h.Upstream.Allow.With(host), BodyCap: -1, Curves: h.Upstream.Curves,
		})
	}
}

// headroomURL is the configured sidecar address, used when the settings row
// holds none.
func headroomURL(h *providers.HTTP) string {
	if h.Saver == nil {
		return ""
	}
	return h.Saver.HeadroomURL
}

// keysController is the client-key handler with the codec and the machine id.
func keysController(h *providers.HTTP, db func() *repository.Store) *controllers.KeysController {
	cfg := facades.Config()
	return &controllers.KeysController{
		DB: db, Quota: h.Limits, Codec: identity.NewKeys(cfg.GetString("auth.api_key_secret")),
		Machine: identity.NewMachine(cfg.GetString("auth.machine_id_salt")),
		Dir:     cfg.GetString("app.data_dir"),
		Changed: h.Auth.Keys.Changed, Log: facades.Log(),
	}
}

// oidcController is the single sign-on handler, built from the bound HTTP pieces.
func oidcController(h *providers.HTTP) *controllers.OIDCController {
	return &controllers.OIDCController{
		Auth: h.Auth, Trust: h.Trust, Server: h.Settings, Client: h.SSO,
		Cache:   providers.CacheOf(facades.App()),
		Log:     facades.Log(),
		DB:      func() *repository.Store { return providers.DataOf(facades.App()).Runner.Store() },
		Pending: sso.NewPending(),
		Allow:   sso.ParseAllow(facades.Config().GetString("auth.sso_allow")),
	}
}

// samlController is the SAML handler, built from the bound HTTP pieces. Its
// replay set is one per process, like the pending sign-in store.
func samlController(h *providers.HTTP) *controllers.SAMLController {
	return &controllers.SAMLController{
		Auth: h.Auth, Trust: h.Trust, Server: h.Settings,
		Log:     facades.Log(),
		DB:      func() *repository.Store { return providers.DataOf(facades.App()).Runner.Store() },
		Pending: sso.NewPending(),
		Replay:  saml.NewReplay(),
		Allow:   sso.ParseAllow(facades.Config().GetString("auth.sso_allow")),
	}
}

// routeBase is what the base routes report. The connection count is read per
// request, because the store opens in a runner and the routes are declared
// before any runner starts.
func routeBase(version, commit string) routes.Base {
	return routes.Base{
		Version: version,
		Commit:  commit,
		Connections: func(ctx context.Context) (int64, error) {
			runner := providers.DataOf(facades.App()).Runner
			store := runner.Store()
			if store == nil {
				return 0, errors.New("bootstrap: the store is not open")
			}
			return store.Connections().Count(ctx)
		},
	}
}
