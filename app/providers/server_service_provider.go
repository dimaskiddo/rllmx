package providers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"rllmx/app/access/quota"
	"rllmx/app/llm/zed"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/binding"
	"github.com/goravel/framework/contracts/config"
	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/access/auth"
	"rllmx/app/access/captcha"
	"rllmx/app/access/identity"
	"rllmx/app/access/oidc"
	"rllmx/app/access/saml"
	"rllmx/app/access/sso"
	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/health"
	"rllmx/app/gateway/hermes"
	"rllmx/app/gateway/metrics"
	"rllmx/app/gateway/skill"
	apphttp "rllmx/app/http"
	"rllmx/app/http/controllers"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/freebuff"
	"rllmx/app/llm/oauth/github"
	llmquota "rllmx/app/llm/quota"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/file"
	"rllmx/app/platform/network"
	"rllmx/app/platform/settings"
	"rllmx/app/store/repository"
)

// jwtFile holds the generated session secret when JWT_SECRET is unset.
const jwtFile = "jwt-secret"

// BindingHTTP is the container key of the HTTP pieces the server, the global
// middleware and the route tables share.
const BindingHTTP = "rllmx.http"

// HTTP is what the binding holds: the checked settings, the one guard whose
// table every route is declared through, which peers are believed as proxies,
// and the authenticator the guard asks who is calling.
type HTTP struct {
	Settings *settings.Server
	Guard    *apphttp.Guard
	Trust    apphttp.Trust
	Auth     *auth.Authenticator
	// SSO is the guarded identity-provider client and the sign-in store.
	SSO *sso.Client
	// Limits is every key's budget, read from the store and cached; the key API
	// writes it and the pipelines check it.
	Limits *quota.Limits
	// Registry is the provider registry, validated at the bind: a bad data file
	// stops the start.
	Registry *upstream.Registry
	// Checker asks a provider whether a key works. It dials through the guard.
	Checker *controllers.Checker
	// Capabilities resolves what a model can do, validated at the bind.
	Capabilities *upstream.Capabilities
	// Health is the per-provider breaker and latency table the pipeline feeds.
	Health *health.Tracker
	// Stream holds the timers every streamed answer runs under.
	Stream *settings.Stream
	// Upstream is the outbound network setting: connect timeout and allow list.
	Upstream *settings.Upstream
	// Media is the checked MEDIA_* configuration.
	Media *settings.Media
	// Saver is the checked SAVER_* configuration.
	Saver *settings.Saver
	// OAuth runs account logins. Its listeners stop with the server.
	OAuth *oauth.Service
	// Refresher renews the tokens of logged-in accounts.
	Refresher *oauth.Refresher
	// Minter makes the Copilot token of a GitHub account.
	Minter *github.Minter
	// Google finds a Cloud Code project and trades a Vertex key for a token.
	Google *Google
	// Zed exchanges a Zed account's token for a hosted one and reads its catalog.
	Zed *zed.Client
	// Freebuff claims the session its backend binds an account to.
	Freebuff *freebuff.Sessions
	// OAuthSettings is the checked OAUTH_* configuration.
	OAuthSettings *settings.OAuth
	// Captcha verifies the challenge a remote sign-in passes. nil asks none.
	Captcha *captcha.Verifier
	// CaptchaKey is the public site key the sign-in page renders the widget with.
	CaptchaKey string
	// Metrics is the checked METRICS_* configuration.
	Metrics *settings.Metrics
	// Meter holds the counters GET /metrics serves. Every finished request
	// records into it through the usage sink.
	Meter *metrics.Registry
	// ClaudeCLI is the claude-cli/x.y.z fingerprint the Claude channel carries.
	ClaudeCLI *upstream.ClaudeCLI
	// Pipe builds the request pipeline on first use; the probe runner shares
	// it, so one pipeline holds the rotation and the caches.
	Pipe *PipelineSource
	// Probes probes a connection's models in the background and answers the
	// dashboard's explicit tests.
	Probes *ProbeSource
	// Prices prices a request and ranks a suggestion; one table, two readers.
	Prices *PriceSource
	// Snapshot is the one view of aliases, nodes and combos; a catalog write
	// invalidates it, so the next request routes the change.
	Snapshot *SnapshotSource
	// Rotation is each combo's round-robin position and Sticky its last smart
	// choice; a combo write resets both.
	Rotation *combo.Rotation
	Sticky   *combo.Sticky
	// Inflight counts the requests running now, for the usage feed.
	Inflight *health.Inflight
	// Quota answers what is left of each connection's plan, for the dashboard.
	Quota *llmquota.Reader
	// CatalogSync writes and installs the synced model catalog.
	CatalogSync *CatalogSync
	// Skills is the one registry of installed skills: the request pipeline reads
	// it and the dashboard writes it, so a change made there is seen by the next
	// request.
	Skills *skill.Registry
	// Hermes reads the memory a local agent keeps, for the skill that injects it.
	Hermes *hermes.Reader
}

// HTTPOf returns the bound HTTP pieces. A build that failed panics with the
// provider's own error, which the start prints as one line; a missing binding
// means the provider is not registered, a bug in the program, and panics too.
func HTTPOf(app foundation.Application) *HTTP {
	v, err := app.Make(BindingHTTP)
	if err != nil {
		panic(err)
	}
	h, ok := v.(*HTTP)
	if !ok || h == nil {
		panic("providers: " + BindingHTTP + " is not bound")
	}
	return h
}

// ResetCombo forgets a combo's rotation and sticky choice, for a combo whose
// members or name just changed.
func (h *HTTP) ResetCombo(names ...string) {
	for _, n := range names {
		h.Rotation.Reset(n)
		h.Sticky.Drop(n)
	}
}

// ServerServiceProvider owns the HTTP server. Register binds, Boot does
// nothing, and the work (checking the route table, opening the socket, serving,
// stopping) is in the runner, so an Artisan command that boots this provider
// takes no port.
type ServerServiceProvider struct{}

// Relationship names what the provider binds and what it reads.
func (r *ServerServiceProvider) Relationship() binding.Relationship {
	return binding.Relationship{
		Bindings:     []string{BindingHTTP},
		Dependencies: []string{binding.Config, binding.Log, binding.Route, binding.Cache, binding.Hash, BindingData},
		ProvideFor:   []string{},
	}
}

// Register binds the settings, the guard and the authenticator.
func (r *ServerServiceProvider) Register(app foundation.Application) {
	app.Singleton(BindingHTTP, func(app foundation.Application) (any, error) {
		// A typed nil would not read as nil through the interface.
		h, err := buildHTTP(app)
		if err != nil {
			return nil, err
		}
		return h, nil
	})
}

// buildHTTP loads every setting the server reads and wires what they build. The
// store opens later, in the data runner, so what needs it takes a closure.
func buildHTTP(app foundation.Application) (*HTTP, error) {
	cfg := app.MakeConfig()
	if cfg == nil {
		return nil, errors.New("providers: the config facade is not set")
	}
	s, err := settings.LoadServer(cfg)
	if err != nil {
		return nil, err
	}
	var logf func(string, ...any)
	if l := app.MakeLog(); l != nil {
		logf = l.Warningf
	}
	secret, err := sessionSecretSource(cfg, logf)
	if err != nil {
		return nil, err
	}
	db := func() *repository.Store { return DataOf(app).Runner.Store() }
	upstreamCfg, err := settings.LoadUpstream(cfg)
	if err != nil {
		return nil, err
	}
	registry, err := upstream.Default()
	if err != nil {
		return nil, err
	}
	capabilities, err := upstream.LoadCapabilities(registry)
	if err != nil {
		return nil, err
	}
	rest, err := loadRest(cfg)
	if err != nil {
		return nil, err
	}
	trust := apphttp.NewTrust(s.TrustedProxies)
	adminName := auth.AdminName(cfg.GetString("auth.admin_username"))
	if _, _, err := auth.CleanUser(adminName, ""); err != nil {
		return nil, fmt.Errorf("providers: AUTH_ADMIN_USERNAME: %w", err)
	}
	a := newAuthenticator(app, cfg, db, secret, s, trust)
	login, refresher, minter, google, hosted, freebuff := newLogin(app, registry, rest.oauth, upstreamCfg)
	pipe := NewPipelineSource(app)
	h := &HTTP{
		Pipe:          pipe,
		Probes:        NewProbeSource(app, pipe),
		Prices:        NewPriceSource(app),
		Snapshot:      NewSnapshotSource(app),
		Rotation:      combo.NewRotation(),
		Sticky:        combo.NewSticky(),
		Settings:      s,
		Guard:         apphttp.NewGuard(a, s.TrustLoopback),
		Trust:         trust,
		Auth:          a,
		Limits:        quota.NewLimits(db, time.Now),
		SSO:           sso.NewClient(upstreamCfg.Allow),
		Registry:      registry,
		Checker:       controllers.NewChecker(upstreamCfg.Allow),
		Capabilities:  capabilities,
		Health:        health.NewTracker(time.Now),
		Inflight:      health.NewInflight(time.Now),
		Stream:        rest.stream,
		Upstream:      upstreamCfg,
		Media:         rest.media,
		Saver:         rest.saver,
		OAuth:         login,
		Refresher:     refresher,
		Minter:        minter,
		Google:        google,
		Zed:           hosted,
		Freebuff:      freebuff,
		ClaudeCLI:     upstream.NewClaudeCLI(upstreamCfg.ClaudeCLIVersion),
		OAuthSettings: rest.oauth,
		Metrics:       rest.metrics,
		Meter:         metrics.New(time.Now),
	}
	if rest.captcha.Enabled() {
		h.CaptchaKey = rest.captcha.SiteKey
		h.Captcha = captcha.New(rest.captcha.Provider, rest.captcha.Secret, network.NewClient(network.Options{
			ConnectTimeout: upstreamCfg.ConnectTimeout, MaxRedirects: -1, BodyCap: 64 << 10,
		}))
	}
	h.Quota = newQuotaReader(h, cacheOrNil(app))
	if err := wireCatalogSync(h, app, cfg); err != nil {
		return nil, err
	}
	return h, nil
}

// serverSettings are the stream, login, media and saver settings, which only
// their own consumers read.
type serverSettings struct {
	stream  *settings.Stream
	oauth   *settings.OAuth
	media   *settings.Media
	saver   *settings.Saver
	captcha *settings.Captcha
	metrics *settings.Metrics
}

// loadRest reads the groups, refusing the start on the first bad value.
func loadRest(cfg config.Config) (serverSettings, error) {
	var out serverSettings
	var err error
	if out.stream, err = settings.LoadStream(cfg); err != nil {
		return out, err
	}
	if out.oauth, err = settings.LoadOAuth(cfg); err != nil {
		return out, err
	}
	if out.media, err = settings.LoadMedia(cfg); err != nil {
		return out, err
	}
	if out.captcha, err = settings.LoadCaptcha(cfg); err != nil {
		return out, err
	}
	if out.metrics, err = settings.LoadMetrics(cfg); err != nil {
		return out, err
	}
	out.saver, err = settings.LoadSaver(cfg)
	return out, err
}

// newAuthenticator builds the dashboard and API-key authenticator, with the
// checks that say whether a single sign-on setting is usable.
func newAuthenticator(app foundation.Application, cfg config.Config, db func() *repository.Store, secret func() []byte, s *settings.Server, trust apphttp.Trust) *auth.Authenticator {
	keys := auth.Keys{DB: db, Cache: cacheOrNil(app), Codec: identity.NewKeys(cfg.GetString("auth.api_key_secret"))}
	a := auth.NewAuthenticator(
		auth.NewStore(db),
		&keys,
		app.MakeHash(),
		cfg.GetString("auth.initial_password"),
		secret,
		s.TrustLoopback,
		cfg.GetBool("auth.cookie_secure"),
	)
	a.OIDCUsable = func(issuer, clientID, secret string) bool {
		return oidc.Settings{Issuer: issuer, ClientID: clientID, ClientSecret: secret}.Usable()
	}
	a.SAMLUsable = func(entryPoint, cert, idpEntity string) bool {
		return saml.Settings{EntryPoint: entryPoint, Cert: cert, IDPEntityID: idpEntity}.Usable()
	}
	a.SSOAdmin = sso.ParseAdmins(cfg.GetString("auth.sso_admin")).IsAdmin
	a.ClientAddr = trust.ClientAddr
	a.SessionTTL = s.SessionTTL
	a.AdminName = auth.AdminName(cfg.GetString("auth.admin_username"))
	users := auth.NewUserRepo(db, s.MaxUsers)
	users.Admin = a.AdminName
	a.Users = auth.NewUsers(users)
	return a
}

// wireCatalogSync gives the HTTP binding its model catalog sync, which owns the
// catalog file in DATA_DIR and fetches through the guarded client, and the one
// registry of installed skills, which lives in the same folder.
func wireCatalogSync(h *HTTP, app foundation.Application, cfg config.Config) error {
	dataDir, err := settings.ResolveDataDir(cfg.GetString("app.data_dir"))
	if err != nil {
		return err
	}
	h.Skills = skill.NewRegistry(filepath.Join(dataDir, "skills"), skill.Builtin())
	h.Hermes = &hermes.Reader{Home: cfg.GetString("hermes.home")}
	h.CatalogSync = newCatalogSync(h.Capabilities, dataDir, func() *http.Client {
		return network.NewClient(network.Options{
			ConnectTimeout: h.Upstream.ConnectTimeout, MaxRedirects: 3, Allow: h.Upstream.Allow,
			BodyCap: upstream.MaxCatalogBytes, Curves: h.Upstream.Curves,
		})
	}, app.MakeLog())
	h.CatalogSync.Enabled = cfg.GetBool("catalog.sync")
	return nil
}

// sessionSecretSource returns a getter for the session secret. JWT_SECRET wins;
// with none set, DATA_DIR/jwt-secret is read on first use, because the data
// runner creates it after every provider has registered.
func sessionSecretSource(cfg config.Config, logf func(format string, args ...any)) (func() []byte, error) {
	if raw := cfg.GetString("jwt.secret"); raw != "" {
		if err := settings.CheckJWT(cfg); err != nil {
			return nil, err
		}
		return func() []byte { return []byte(raw) }, nil
	}
	dir, err := settings.ResolveDataDir(cfg.GetString("app.data_dir"))
	if err != nil {
		return nil, err
	}
	return fileSecret(dir, logf), nil
}

// fileSecret reads DATA_DIR/jwt-secret on first use and keeps the value. A
// failed read is not kept, so a file that was briefly unreadable works on the
// next request; each failure is logged once per change of cause, never the value.
func fileSecret(dir string, logf func(format string, args ...any)) func() []byte {
	var mu sync.Mutex
	var secret []byte
	var lastErr string
	return func() []byte {
		mu.Lock()
		defer mu.Unlock()
		if secret != nil {
			return secret
		}
		s, err := readJWTFile(dir)
		if err != nil {
			if msg := err.Error(); logf != nil && msg != lastErr {
				lastErr = msg
				logf("session secret unavailable, every session is refused: %v", err)
			}
			return nil
		}
		secret = s
		return secret
	}
}

// EnsureSessionSecret creates DATA_DIR/jwt-secret when JWT_SECRET is unset and
// the file is missing. The data runner calls it once the store is open, so no
// provider bind and no command writes it.
func EnsureSessionSecret(cfg config.Config) error {
	if cfg.GetString("jwt.secret") != "" {
		return nil
	}
	dir, err := settings.ResolveDataDir(cfg.GetString("app.data_dir"))
	if err != nil {
		return err
	}
	_, err = readJWTFile(dir)
	return err
}

// readJWTFile reads DATA_DIR/jwt-secret, creating it once with 32 random bytes in
// hex. A short existing file is an error: silently replacing it would end every
// session without a reason.
func readJWTFile(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, jwtFile)
	b, err := os.ReadFile(path)
	if err == nil {
		raw := strings.TrimSpace(string(b))
		if len(raw) < 32 {
			return nil, errors.New("providers: DATA_DIR/jwt-secret is shorter than 32 characters; set JWT_SECRET or remove the file")
		}
		return []byte(raw), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("providers: read %s: %w", jwtFile, err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, errors.New("providers: the system random source failed")
	}
	secret := hex.EncodeToString(raw)
	if err := file.WritePrivate(dataDir, jwtFile, []byte(secret)); err != nil {
		return nil, err
	}
	return []byte(secret), nil
}

// Boot has nothing to start; see the type comment.
func (r *ServerServiceProvider) Boot(app foundation.Application) {}

// Runners returns the HTTP server. The stock goravel:http runner is switched
// off in config/app.go, because it builds a server without our timeouts.
func (r *ServerServiceProvider) Runners(app foundation.Application) []foundation.Runner {
	h := HTTPOf(app)
	route := app.MakeRoute()
	server := apphttp.NewServer(h.Settings, apphttp.Rewrite(route), h.Guard, route.GetRoutes, app.MakeLog())
	server.WaitFor(DataOf(app).Gate)
	return []foundation.Runner{server, newOAuthRunner(app, h, h.OAuthSettings), newClaudeRunner(app, h), newQuotaPingRunner(app, h), newProbeRunner(app, h.Probes, h), newCatalogRunner(app, h.CatalogSync, h.CatalogSync.Enabled)}
}

// cacheOrNil returns the grouped cache, or nil when the cache provider is not
// registered: a key lookup then always reaches the store, which is correct and
// only slower.
func cacheOrNil(app foundation.Application) *cache.Cache {
	v, err := app.Make(BindingCache)
	c, ok := v.(*cache.Cache)
	if err != nil || !ok {
		return nil
	}
	return c
}
