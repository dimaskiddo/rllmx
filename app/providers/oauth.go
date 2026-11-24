package providers

import (
	"context"
	"strings"
	"time"

	"rllmx/app/llm/zed"
	"rllmx/app/platform/network"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/dialects"
	"rllmx/app/llm/oauth/freebuff"
	"rllmx/app/llm/oauth/github"
	"rllmx/app/llm/oauth/google"
	"rllmx/app/llm/oauth/xiaomi"
	"rllmx/app/llm/quota"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/settings"
	"rllmx/app/store/data"
)

// newLogin builds the account login service and the refresher that keeps its
// tokens alive. The store opens in a runner after every provider registered, so
// accounts are looked up on use.
func newLogin(app foundation.Application, reg *upstream.Registry, cfg *settings.OAuth, up *settings.Upstream) (*oauth.Service, *oauth.Refresher, *github.Minter, *Google, *zed.Client, *freebuff.Sessions) {
	sessions := oauth.NewSessions()
	listeners := &oauth.Listeners{Host: cfg.CallbackHost, Sessions: sessions, Log: app.MakeLog()}
	tokens := oauth.NewTokenClient(up.Allow)
	table := dialects.Default()
	tokens.Dialects = table
	svc := &oauth.Service{
		Registry: reg, ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Sessions: sessions,
		Tokens: tokens, Listeners: listeners, Log: app.MakeLog(), Now: time.Now, Dialects: table,
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
			f, err := oauth.FlowFor(reg, cfg.ClientID, cfg.ClientSecret, provider)
			return f, err == nil
		},
		Tokens: tokens, Cache: cacheOrNil(app), Log: app.MakeLog(), Now: time.Now,
		Dialects: table, SlowGap: cfg.RefreshGoogleDelay,
		Store: func() oauth.Store {
			store := DataOf(app).Runner.Store()
			if store == nil {
				return nil
			}
			return store.Connections()
		},
	}
	minter := &github.Minter{
		Registry: reg, Tokens: tokens, Cache: cacheOrNil(app), Log: app.MakeLog(), Now: time.Now,
		Store: func() github.PSDStore {
			store := DataOf(app).Runner.Store()
			if store == nil {
				return nil
			}
			return store.Connections()
		},
	}
	google := &Google{
		Projects: &google.ProjectResolver{
			Registry: reg, Tokens: tokens, Cache: cacheOrNil(app), Log: app.MakeLog(),
			Store: func() google.ProjectStore {
				store := DataOf(app).Runner.Store()
				if store == nil {
					return nil
				}
				return store.Connections()
			},
		},
		Vertex: google.VertexTokens{Minter: &google.GoogleMinter{Tokens: tokens, Cache: cacheOrNil(app), Log: app.MakeLog(), Now: time.Now}},
		Mimo:   &xiaomi.MimoCookies{Tokens: tokens, Cache: cacheOrNil(app), Log: app.MakeLog()},
		Quota:  newQuotaGate(reg, tokens.HTTP),
	}
	hosted := &zed.Client{HTTP: network.NewClient(network.Options{MaxRedirects: 0, Allow: up.Allow, BodyCap: -1}), Cache: cacheOrNil(app)}
	fb := &freebuff.Sessions{HTTP: network.NewClient(network.Options{MaxRedirects: 0, Allow: up.Allow, BodyCap: -1}), Cache: cacheOrNil(app), Log: app.MakeLog()}
	return svc, refresher, minter, google, hosted, fb
}

// Google holds what the Google providers need beside the common login: the
// resolver that finds a Cloud Code account's project, the source that trades a
// Vertex key for a token, and the Antigravity quota gate.
type Google struct {
	Projects *google.ProjectResolver
	Vertex   google.VertexTokens
	Quota    *quota.Gate
	// Mimo trades a MiMo desktop account's pass token for a session cookie.
	Mimo *xiaomi.MimoCookies
}

// newQuotaGate builds the Antigravity quota gate from the provider's own data: the
// calls it makes and the client it says it is. A registry without Antigravity
// gets a gate that never learns anything, which skips nothing.
func newQuotaGate(reg *upstream.Registry, client quota.HTTP) *quota.Gate {
	e, ok := reg.ByID("antigravity")
	if !ok || e.OAuth == nil {
		return quota.New(nil)
	}
	f := &quota.Fetcher{Client: client, URL: quota.Endpoints{
		Models: e.OAuth.Endpoints[upstream.EndpointQuotaModels], Summary: e.OAuth.Endpoints[upstream.EndpointQuotaSummary],
		Load: e.OAuth.Endpoints[upstream.EndpointLoadCodeAssist], UserAgent: e.Transport.Headers["User-Agent"], Version: quotaVersion(e.Transport.Headers["User-Agent"]),
	}}
	return quota.New(f.Fetch)
}

// quotaVersion reads the version out of "antigravity/hub/2.33.0 darwin/arm64".
func quotaVersion(agent string) string {
	head, _, _ := strings.Cut(agent, " ")
	if i := strings.LastIndexByte(head, '/'); i >= 0 {
		return head[i+1:]
	}
	return ""
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
