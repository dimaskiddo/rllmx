package providers

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	"rllmx/app/access/identity"
	"rllmx/app/access/oidc"
	"rllmx/app/access/saml"
	"rllmx/app/access/sso"
	apphttp "rllmx/app/http"
	"rllmx/app/http/controllers"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/freebuff"
	"rllmx/app/llm/oauth/github"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/platform/file"
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
	// Stream holds the timers every streamed answer runs under.
	Stream *settings.Stream
	// Upstream is the outbound network setting: connect timeout and allow list.
	Upstream *settings.Upstream
	// Media is the checked MEDIA_* configuration.
	Media *settings.Media
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
	// ClaudeCLI is the claude-cli/x.y.z fingerprint the Claude channel carries.
	ClaudeCLI *upstream.ClaudeCLI
}

// HTTPOf returns the bound HTTP pieces. A missing binding means the provider is
// not registered, which is a bug in the program, so it panics.
func HTTPOf(app foundation.Application) *HTTP {
	v, err := app.Make(BindingHTTP)
	h, ok := v.(*HTTP)
	if err != nil || !ok || h == nil {
		panic(fmt.Sprintf("providers: %s is not bound: %v", BindingHTTP, err))
	}
	return h
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
		// The store opens in the data runner, after every provider registered, so
		// the authenticator reads it through a closure rather than a value.
		db := func() *repository.Store { return DataOf(app).Runner.Store() }
		keys := auth.Keys{DB: db, Cache: cacheOrNil(app), Codec: identity.NewKeys(cfg.GetString("auth.api_key_secret"))}
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
		streamCfg, err := settings.LoadStream(cfg)
		if err != nil {
			return nil, err
		}
		oauthCfg, err := settings.LoadOAuth(cfg)
		if err != nil {
			return nil, err
		}
		mediaCfg, err := settings.LoadMedia(cfg)
		if err != nil {
			return nil, err
		}
		allowSSO := sso.ParseAllow(cfg.GetString("auth.sso_allow"))
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
			return oidc.Settings{Issuer: issuer, ClientID: clientID, ClientSecret: secret}.Configured()
		}
		a.SAMLUsable = func(entryPoint, cert, idpEntity string) bool {
			s := saml.Settings{EntryPoint: entryPoint, Cert: cert, IDPEntityID: idpEntity}
			if !s.Configured() || idpEntity == "" {
				return false
			}
			_, err := s.Validate()
			return err == nil
		}
		a.SSOAllow = allowSSO.Allows
		trust := apphttp.NewTrust(s.TrustedProxies)
		a.ClientAddr = trust.ClientAddr
		login, refresher, minter, google, hosted, freebuff := newLogin(app, registry, oauthCfg, upstreamCfg)
		return &HTTP{
			Settings:      s,
			Guard:         apphttp.NewGuard(a, s.TrustLoopback),
			Trust:         trust,
			Auth:          a,
			Limits:        quota.NewLimits(db, time.Now),
			SSO:           sso.NewClient(upstreamCfg.Allow),
			Registry:      registry,
			Checker:       controllers.NewChecker(upstreamCfg.Allow),
			Capabilities:  capabilities,
			Stream:        streamCfg,
			Upstream:      upstreamCfg,
			Media:         mediaCfg,
			OAuth:         login,
			Refresher:     refresher,
			Minter:        minter,
			Google:        google,
			Zed:           hosted,
			Freebuff:      freebuff,
			ClaudeCLI:     upstream.NewClaudeCLI(upstreamCfg.ClaudeCLIVersion),
			OAuthSettings: oauthCfg,
		}, nil
	})
}

// sessionSecretSource returns a getter for the session secret. JWT_SECRET wins;
// with none set, DATA_DIR/jwt-secret is read on first use, because the data
// runner creates it after every provider has registered.
func sessionSecretSource(cfg config.Config, logf func(format string, args ...any)) (func() []byte, error) {
	if raw := cfg.GetString("jwt.secret"); raw != "" {
		if len(raw) < 32 {
			return nil, errors.New("providers: JWT_SECRET: must be at least 32 characters")
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
	return []foundation.Runner{server, newOAuthRunner(app, h, h.OAuthSettings), newClaudeRunner(app, h)}
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
