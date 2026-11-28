package bootstrap

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/goravel/framework/contracts/console"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	contractsfoundation "github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/foundation/configuration"
	"github.com/goravel/framework/contracts/schedule"
	"github.com/goravel/framework/foundation"

	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	"rllmx/app/access/saml"
	"rllmx/app/access/sso"
	"rllmx/app/facades"
	"rllmx/app/gateway/skill"
	"rllmx/app/http/controllers"
	"rllmx/app/http/middleware"
	appfile "rllmx/app/platform/file"
	"rllmx/app/platform/network"
	"rllmx/app/platform/upgrade"
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
			challenge := ""
			if h.Captcha != nil {
				challenge = h.Captcha.Provider
			}
			middleware.Stack(m, middleware.Options{
				Guard:       h.Guard,
				Trust:       h.Trust,
				Log:         facades.Log(),
				MaxBody:     int64(h.Settings.MaxBodyMB) << 20,
				LoginPolicy: middleware.LoginPolicy(challenge),
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
	base.Auth.Captcha, base.Auth.CaptchaKey = h.Captcha, h.CaptchaKey
	base.Auth.Log = facades.Log()
	base.OIDC = oidcController(h)
	base.SAML = samlController(h)
	base.Providers = &controllers.ProvidersController{Registry: h.Registry, OAuth: h.OAuth, DB: db, Checker: h.Checker, Refresher: h.Refresher, Enqueue: h.Probes.Enqueue, Probe: h.Probes.Prober, ClaudeCLI: h.ClaudeCLI,
		LiveCatalog: providers.CatalogGuard(h, facades.App()),
		Fetch:       guardedFetch(version, "application/json", 8<<20)}
	base.Nodes = &controllers.NodesController{Registry: h.Registry, DB: db, Checker: h.Checker, Changed: h.Snapshot.Invalidate}
	base.OAuth = &controllers.OAuthController{Service: h.OAuth, Auth: h.Auth, Server: h.Settings}
	base.Pages = &controllers.PagesController{Files: web.Static}
	base.Health = &controllers.HealthController{DB: db, Tracker: h.Health}
	// The dashboard's two skill-aware controllers and the request pipeline share
	// one registry, so a setting the Studio writes is validated by the same
	// manifests a request routes with, and a skill it saves is read by the next one.
	dir := providers.DataOf(facades.App()).Settings.DataDir
	skills := h.Skills
	base.Settings = &controllers.SettingsController{DB: db, Auth: h.Auth, Skills: skills, Secure: h.Trust.SecureRequest}
	base.About = &controllers.AboutController{
		Version: version, Commit: commit,
		Fetch:   guardedFetch(version, "application/vnd.github+json", 64<<10),
		Upgrade: func(ctx context.Context) (string, string, error) { return installRelease(version, ctx) },
		Restart: restartBinary,
	}
	base.Logs = &controllers.LogsController{Book: config.ConsoleLog()}
	if h.Metrics.Enabled() {
		base.Metrics = &controllers.MetricsController{
			Reg: h.Meter, Token: h.Metrics.Token, Username: h.Metrics.Username, Password: h.Metrics.Password,
			Inflight: h.Inflight, DB: db, Users: h.Auth.Users, Version: version, Commit: commit,
			Trust: h.Trust, Limiter: h.Auth.Limiter,
		}
	}
	base.Keys = keysController(h, db)
	clientRoutes(&base, h, db)
	dashboardRoutes(&base, h, db, skills, dir)
	routes.Web(facades.Route(), h.Guard, base)
}

// clientRoutes fills the controllers an API client calls: chat, media and the
// model lists.
func clientRoutes(base *routes.Base, h *providers.HTTP, db func() *repository.Store) {
	media := providers.NewMediaSource(facades.App())
	base.Chat = &controllers.ChatController{Pipe: h.Pipe.Get, Log: facades.Log(), Standing: clientStanding(h)}
	base.Media = &controllers.MediaController{
		EmbeddingsPipe: media.Get, ImagesPipe: media.Get, SpeechPipe: media.Get,
		TranscribePipe: media.Get, VoicesPipe: media.Get, VideosPipe: media.Get,
		SearchPipe: media.Get, FetchPipe: media.Get, Log: facades.Log(),
	}
	base.ClientUsage = &controllers.ClientUsageController{Standing: clientStanding(h)}
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

// clientStanding reads a key's budget standing over the same analytics store and
// writer the budget check uses, so what a client is told is what would refuse it.
func clientStanding(h *providers.HTTP) func(context.Context, string) (quota.Standings, error) {
	return func(ctx context.Context, keyID string) (quota.Standings, error) {
		return providers.Budget(h.Limits, providers.DataOf(facades.App()).Runner).Standing(ctx, keyID)
	}
}

// dashboardRoutes fills the controllers behind the dashboard's own pages.
func dashboardRoutes(base *routes.Base, h *providers.HTTP, db func() *repository.Store, skills *skill.Registry, dir string) {
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
		Users: h.Auth.Users,
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
	base.Skills = &controllers.SkillsController{DB: db, Dir: dir, Reg: skills, Log: facades.Log()}
	base.Users = &controllers.UsersController{Users: h.Auth.Users, Pass: h.Auth.Pass, Log: facades.Log()}
}

// guardedFetch reads one https address through the guarded client. The cap is
// the most it will hold; a release archive needs a large one, a release answer
// a small one.
func guardedFetch(version, accept string, cap int64) func(context.Context, string) ([]byte, error) {
	client := network.NewClient(network.Options{
		ConnectTimeout: 10 * time.Second, MaxRedirects: 4, BodyCap: cap, DisableKeepAlives: true,
	})
	return func(ctx context.Context, address string) ([]byte, error) {
		req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("User-Agent", "rllmx/"+version)
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != nethttp.StatusOK {
			return nil, fmt.Errorf("fetch: status %d", res.StatusCode)
		}
		// A release body larger than the cap is refused, not cut: a cut archive
		// would pass the size check below and fail its checksum instead.
		body, err := appfile.ReadCapped(res.Body, cap)
		if err != nil {
			return nil, err
		}
		return body, nil
	}
}

// githubJSON is the Accept value the release API answers; it refuses any other.
const githubJSON = "application/vnd.github+json"

// releaseAccept names what an address is read as: the release API as JSON, a
// release file as bytes.
func releaseAccept(address string) string {
	if strings.HasPrefix(address, "https://api.github.com/") {
		return githubJSON
	}
	return "application/octet-stream"
}

// releaseFetch reads the release answer and its files, each with its own Accept.
func releaseFetch(version string) func(context.Context, string) ([]byte, error) {
	meta := guardedFetch(version, githubJSON, 1<<20)
	file := guardedFetch(version, "application/octet-stream", 64<<20)
	return func(ctx context.Context, address string) ([]byte, error) {
		if releaseAccept(address) == githubJSON {
			return meta(ctx, address)
		}
		return file(ctx, address)
	}
}

// installRelease replaces this binary with the newest release for this platform
// and answers its version and the path it now lives at.
func installRelease(version string, ctx context.Context) (latest, exe string, err error) {
	res, err := upgrade.Install(ctx, upgrade.Options{Version: version, Fetch: releaseFetch(version)})
	if err != nil {
		return "", "", err
	}
	return res.Latest, res.Exe, nil
}

// pendingExe is the binary an update asked to start once this process has
// stopped.
var pendingExe atomic.Pointer[string]

// restartBinary stops the application the way a signal would, so the server
// drains and the usage writer flushes, and leaves the new binary to Relaunch.
// It names the path the install returned: the running file was renamed aside,
// so asking the system for its own path now would name the old one.
func restartBinary(exe string) {
	pendingExe.Store(&exe)
	if err := facades.App().Shutdown(); err != nil {
		facades.Log().Warningf("upgrade: the application did not stop cleanly: %v", err)
	}
}

// Relaunch replaces the stopped process with the updated binary when an update
// asked for one. It returns when none did, or when the start failed.
func Relaunch() {
	relaunch(upgrade.Restart)
}

func relaunch(start func(string) error) {
	exe := pendingExe.Swap(nil)
	if exe == nil {
		return
	}
	if err := start(*exe); err != nil {
		facades.Log().Errorf("upgrade: the new binary could not be started: %v", err)
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
		Budget:  providers.Budget(h.Limits, providers.DataOf(facades.App()).Runner),
		Machine: identity.NewMachine(cfg.GetString("auth.machine_id_salt")),
		Dir:     cfg.GetString("app.data_dir"),
		Users:   h.Auth.Users,
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
