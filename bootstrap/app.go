package bootstrap

import (
	"context"
	"errors"

	"github.com/goravel/framework/contracts/console"
	contractsschema "github.com/goravel/framework/contracts/database/schema"
	contractsfoundation "github.com/goravel/framework/contracts/foundation"
	"github.com/goravel/framework/contracts/foundation/configuration"
	"github.com/goravel/framework/contracts/schedule"
	"github.com/goravel/framework/foundation"

	"rllmx/app/access/saml"
	"rllmx/app/access/sso"
	"rllmx/app/facades"
	"rllmx/app/http/controllers"
	"rllmx/app/http/middleware"
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
		WithRouting(func() {
			h := providers.HTTPOf(facades.App())
			base := routeBase(version, commit)
			base.Auth = controllers.NewAuthController(h.Auth, h.Trust)
			base.OIDC = oidcController(h)
			base.SAML = samlController(h)
			db := func() *repository.Store { return providers.DataOf(facades.App()).Runner.Store() }
			base.Providers = &controllers.ProvidersController{Registry: h.Registry, DB: db, Checker: h.Checker, Refresher: h.Refresher}
			base.Nodes = &controllers.NodesController{Registry: h.Registry, DB: db, Checker: h.Checker}
			base.Chat = &controllers.ChatController{Pipe: providers.NewPipelineSource(facades.App()).Get, Log: facades.Log()}
			base.Models = &controllers.ModelsController{Catalog: providers.NewCatalogSource(facades.App()).Get, Log: facades.Log()}
			base.OAuth = &controllers.OAuthController{Service: h.OAuth, Auth: h.Auth, Server: h.Settings}
			base.Callback = &controllers.CallbackController{Files: web.Static}
			routes.Web(facades.Route(), h.Guard, base)
		}).
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
