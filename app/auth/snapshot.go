package auth

import (
	"context"
	"errors"
	"strconv"
	"time"

	"rllmx/app/document"
	"rllmx/app/repository"
	"rllmx/app/ttl"
)

const snapshotTTL = 5 * time.Second

// ScopeSession is the kv scope holding the session epoch. It is additive, so a
// 9router database opens unchanged.
const ScopeSession = "rllmAuth"

// EpochKey is the kv key of the session epoch inside ScopeSession.
const EpochKey = "sessionEpoch"

// Settings is the auth-relevant projection of the settings row.
type Settings struct {
	RequireLogin  bool
	RequireAPIKey bool
	AuthMode      string
	SSOType       string
	// Password is the stored hash, empty when none is stored.
	Password string
	// Epoch is the session epoch; a bump ends every issued session.
	Epoch string
	// The identity-provider settings, so the sign-in mode is resolved from one
	// snapshot rather than a second read.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCScopes       string
	// The SAML identity provider, for the same reason.
	SAMLEntryPoint string
	SAMLCert       string
	SAMLIdPEntity  string
	// SSOAllow is AUTH_SSO_ALLOW, read from the environment at wiring time.
	SSOAllow string
}

// DefaultSettings is what a failed read means: login and key required, nothing
// authenticated, password login.
func DefaultSettings() Settings {
	return Settings{RequireLogin: true, RequireAPIKey: true, AuthMode: "password", SSOType: "oidc", Epoch: "0"}
}

// Store reads the settings row and the session epoch, cached briefly so a
// request path does not query the database per request. A request takes no lock
// to read the cached value.
type Store struct {
	// DB returns the open store, nil before the data runner opened it.
	DB func() *repository.Store
	// Now is the clock; tests replace it.
	Now func() time.Time

	cached *ttl.Value[Settings]
}

// settingsLoadTimeout bounds one read of the settings row and the epoch, which
// no caller's context owns.
const settingsLoadTimeout = 10 * time.Second

// errUnreadable stands for a read that failed or found no open store. The
// cache never keeps it, so the next request tries again.
var errUnreadable = errors.New("auth: settings unavailable")

// NewStore builds the settings reader.
func NewStore(db func() *repository.Store) *Store {
	s := &Store{DB: db, Now: time.Now}
	s.cached = ttl.New(snapshotTTL, func() time.Time { return s.Now() }, func(ctx context.Context) (Settings, error) {
		v, ok := s.read(ctx)
		if !ok {
			return Settings{}, errUnreadable
		}
		return v, nil
	})
	return s
}

// Get returns the current settings, from the cache while it is fresh. A store
// that is not open, or a failed read, gives the strict defaults.
func (s *Store) Get(ctx context.Context) Settings {
	v, err := s.cached.Get(ctx, settingsLoadTimeout)
	if err != nil {
		return DefaultSettings()
	}
	return v
}

// Changed drops the cache, for a write this process just made.
func (s *Store) Changed() { s.cached.Drop() }

// read queries the settings row and the epoch. A store that is not open yet, or
// a read that fails, reports ok=false so the caller falls back to the strict
// defaults rather than to "nothing is required".
func (s *Store) read(ctx context.Context) (Settings, bool) {
	db := s.DB()
	if db == nil {
		return Settings{}, false
	}
	d, err := db.Settings().Effective(ctx)
	if err != nil {
		return Settings{}, false
	}
	v := DefaultSettings()
	v.RequireLogin = boolOr(d, "requireLogin", true)
	v.RequireAPIKey = boolOr(d, "requireApiKey", true)
	if mode, ok := d.GetString("authMode"); ok && mode != "" {
		v.AuthMode = mode
	}
	if t, ok := d.GetString("ssoType"); ok && t != "" {
		v.SSOType = t
	}
	v.Password, _ = d.GetString("password")
	v.OIDCIssuer, _ = d.GetString("oidcIssuerUrl")
	v.OIDCClientID, _ = d.GetString("oidcClientId")
	v.OIDCClientSecret, _ = d.GetString("oidcClientSecret")
	v.OIDCScopes, _ = d.GetString("oidcScopes")
	v.SAMLEntryPoint, _ = d.GetString("samlEntryPoint")
	v.SAMLCert, _ = d.GetString("samlCert")
	v.SAMLIdPEntity, _ = d.GetString("samlIdpEntityId")

	epoch, found, err := db.KV().Get(ctx, ScopeSession, EpochKey)
	if err == nil && found {
		v.Epoch = epoch
	}
	return v, true
}

// BumpEpoch increments the session epoch in one transaction, which ends every
// session issued before it.
func (s *Store) BumpEpoch(ctx context.Context) (string, error) {
	db := s.DB()
	if db == nil {
		return "", repository.ErrNotFound
	}
	n, err := db.KV().Increment(ctx, ScopeSession, EpochKey)
	if err != nil {
		return "", err
	}
	s.Changed()
	return strconv.FormatInt(n, 10), nil
}

// boolOr reads a boolean setting that may be stored as a JSON boolean.
func boolOr(d document.Doc, key string, fallback bool) bool {
	raw, ok := d.GetRaw(key)
	if !ok {
		return fallback
	}
	switch raw {
	case "true":
		return true
	case "false":
		return false
	}
	return fallback
}
