package providers

import (
	"context"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/data"
	"rllmx/app/oauth"
	"rllmx/app/settings"
	"rllmx/app/upstream"
)

// newLogin builds the account login service and the refresher that keeps its
// tokens alive. The store opens in a runner after every provider registered, so
// accounts are looked up on use.
func newLogin(app foundation.Application, reg *upstream.Registry, cfg *settings.OAuth, up *settings.Upstream) (*oauth.Service, *oauth.Refresher) {
	sessions := oauth.NewSessions()
	listeners := &oauth.Listeners{Host: cfg.CallbackHost, Sessions: sessions, Log: app.MakeLog()}
	tokens := oauth.NewTokenClient(up.Allow)
	svc := &oauth.Service{
		Registry: reg, ClientID: cfg.ClientID, Sessions: sessions,
		Tokens: tokens, Listeners: listeners, Log: app.MakeLog(), Now: time.Now,
		Accounts: func() oauth.Accounts {
			store := DataOf(app).Runner.Store()
			if store == nil {
				return nil
			}
			return store.Connections()
		},
	}
	listeners.Handle = svc.Listened
	refresher := &oauth.Refresher{
		Flow: func(provider string) (oauth.Flow, bool) {
			id, ok := reg.Resolve(provider)
			if !ok {
				return oauth.Flow{}, false
			}
			e, _ := reg.ByID(id)
			f, err := oauth.NewFlow(e, cfg.ClientID(id))
			return f, err == nil
		},
		Tokens: tokens, Cache: cacheOrNil(app), Log: app.MakeLog(), Now: time.Now,
		Store: func() oauth.Store {
			store := DataOf(app).Runner.Store()
			if store == nil {
				return nil
			}
			return store.Connections()
		},
	}
	return svc, refresher
}

// oauthRunner owns what the login keeps running: the callback listeners, which
// close with the server, and the background renewal, which waits for the data
// to be ready, then passes every few minutes until shutdown.
type oauthRunner struct {
	listeners *oauth.Listeners
	refresher *oauth.Refresher
	gate      *data.Gate
	enabled   bool
	delay     time.Duration
	ctx       context.Context
	cancel    context.CancelFunc
}

func newOAuthRunner(app foundation.Application, h *HTTP, cfg *settings.OAuth) *oauthRunner {
	ctx, cancel := context.WithCancel(app.Context())
	return &oauthRunner{
		listeners: h.OAuth.Listeners, refresher: h.Refresher, gate: DataOf(app).Gate,
		enabled: cfg.BackgroundRefresh, delay: cfg.RefreshDelay, ctx: ctx, cancel: cancel,
	}
}

func (r *oauthRunner) Signature() string { return "rllmx.oauth" }
func (r *oauthRunner) ShouldRun() bool   { return true }

// Run starts the background renewal once the stores are open. It returns when
// shutdown cancels it, or at once when the renewal is switched off.
func (r *oauthRunner) Run() error {
	if !r.enabled {
		return nil
	}
	if r.gate != nil && !r.gate.Wait(r.ctx) {
		return nil
	}
	r.refresher.Loop(r.ctx, r.delay)
	return nil
}

func (r *oauthRunner) Shutdown() error {
	r.cancel()
	r.listeners.Close()
	return nil
}
