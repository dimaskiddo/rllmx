package oauth

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/access/oidc"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// Errors only this file raises; the others are in flow.go.
var (
	// ErrApp means the request brought an application a provider does not take,
	// or none to a provider that needs one.
	ErrApp = errors.New("oauth: the login needs its own application, or takes none")
	// ErrPublicBase means the login's redirect is the gateway's public address and
	// none is known.
	ErrPublicBase = errors.New("oauth: the login needs APP_URL to build its redirect")
)

// Failure codes a finished login can end with. They are fixed words, never
// provider text.
const (
	CodeDenied  = "access_denied"
	CodeInput   = "invalid_input"
	CodeStorage = "storage"
)

// Failure is a login that ended without an account. Code is one of the fixed
// words above or a token Class.
type Failure struct{ Code string }

func (f *Failure) Error() string { return "oauth: the login failed: " + f.Code }

// Accounts is where a finished login is stored.
type Accounts interface {
	Create(ctx context.Context, in repository.ConnInput, allowOverwrite *bool) (repository.Connection, error)
}

// Service runs logins: Start opens one, Complete and Listened close it.
type Service struct {
	Registry *upstream.Registry
	ClientID func(provider string) string
	// ClientSecret may be nil; it gives the secret of a client that has one.
	ClientSecret func(provider string) string
	Sessions     *Sessions
	Tokens       *TokenClient
	Accounts     func() Accounts
	Listeners    *Listeners
	Log          log.Log
	Now          func() time.Time
	// Dialects are the provider-specific steps; the zero value takes the ones in
	// DefaultDialects.
	Dialects Dialects
}

// Started is what the browser needs to send the user to the provider.
type Started struct {
	AuthURL  string `json:"authUrl"`
	State    string `json:"state"`
	Redirect string `json:"redirect"`
	// Paste is set when the browser that finishes the login cannot reach the
	// redirect, so the user pastes the address it lands on.
	Paste bool `json:"paste"`
}

func (s *Service) flow(provider string) (Flow, error) {
	return FlowFor(s.Registry, s.ClientID, s.ClientSecret, provider)
}

// StartRequest is what opens a login.
type StartRequest struct {
	Provider string
	// Bind ties the login to the dashboard session.
	Bind string
	// Local says whether the browser runs on this machine.
	Local bool
	// RouterPort is the port the router listens on.
	RouterPort int
	// PublicBase is the address this gateway is reached at, or "" when none is
	// known; only a login on the public redirect reads it.
	PublicBase string
	// App is the application that came with the request, or nil.
	App *App
	// Variant names which of a provider's several logins this is, when its data
	// offers more than one: Kiro signs in through AWS SSO or through an identity
	// provider, and only the second needs the user to choose which.
	Variant string
}

// flowFor resolves the login of a request: the provider's own, or the one the
// request's application rebuilds.
func (s *Service) flowFor(provider string, app *App) (Flow, error) {
	f, err := s.flow(provider)
	if err != nil {
		return Flow{}, err
	}
	if f.AppFromAccount != (app != nil) {
		return Flow{}, ErrApp
	}
	if app == nil {
		return f, nil
	}
	if f, err = f.WithApp(*app); err != nil {
		return Flow{}, ErrApp
	}
	return f, nil
}

// Start opens a login.
func (s *Service) Start(req StartRequest) (Started, error) {
	f, err := s.flowFor(req.Provider, req.App)
	if err != nil {
		return Started{}, err
	}
	if f.Dialect != "" {
		if f, err = withVariant(f, req.Variant); err != nil {
			return Started{}, err
		}
	}
	if f.NeedsPublicBase() && req.PublicBase == "" {
		return Started{}, ErrPublicBase
	}
	var verifier, challenge string
	if f.Flow == upstream.FlowCodePKCE && !f.NoPKCE {
		if verifier, err = oidc.Verifier(); err != nil {
			return Started{}, err
		}
		challenge = oidc.Challenge(verifier)
	}
	// A sign-in that makes its own key returns the address and the key together:
	// the address names the public half, and the private one is what the answer
	// is read with.
	var authURL string
	if f.SignedIn() {
		if authURL, verifier, err = zedAuthorize(f); err != nil {
			return Started{}, err
		}
	}
	nonce, err := NewNonce()
	if err != nil {
		return Started{}, err
	}
	redirect := f.RedirectURL(req.RouterPort, req.PublicBase)
	port := f.ListenerPort()
	// A login whose provider answers a scheme no server receives is finished by
	// pasting, whatever this request looks like.
	paste := !req.Local && !f.NeedsPublicBase() || f.Dialect == kiroSocialDialect
	if port != 0 {
		if s.Listeners == nil || !s.Listeners.Acquire(port, f.CallbackPath) {
			paste = true
		}
	}
	state, err := s.Sessions.Put(Session{Provider: f.Provider, Redirect: redirect, Verifier: verifier, Port: port, Bind: req.Bind, App: req.App, Variant: req.Variant})
	if err != nil {
		if port != 0 && s.Listeners != nil {
			s.Listeners.Release(port)
		}
		return Started{}, err
	}
	if authURL == "" {
		authURL = f.URL(redirect, state, challenge, nonce)
	}
	return Started{AuthURL: authURL, State: state, Redirect: redirect, Paste: paste}, nil
}

// Complete finishes a login from the dashboard. The state must belong to a
// pending login that this dashboard session started.
func (s *Service) Complete(ctx context.Context, state string, in Input, bind string) (string, error) {
	return s.finish(ctx, state, in, func(sess Session) bool { return SameBind(sess.Bind, bind) })
}

// Listened finishes a login from a fixed-port listener. The browser carries no
// dashboard cookie there, so the 256-bit state, which only the provider's
// redirect brings back, and the listener's port are the proof.
func (s *Service) Listened(ctx context.Context, port int, state string, in Input) error {
	_, err := s.finish(ctx, state, in, func(sess Session) bool { return sess.Port == port })
	return err
}

func (s *Service) finish(ctx context.Context, state string, in Input, allow func(Session) bool) (string, error) {
	sess, ok := s.Sessions.Begin(state, allow)
	if !ok {
		return "", ErrSession
	}
	if in.State != "" && in.State != state {
		return "", s.end(state, sess, "", CodeInput)
	}
	if in.Failed {
		return "", s.end(state, sess, "", CodeDenied)
	}
	f, err := s.flowFor(sess.Provider, sess.App)
	if err != nil {
		return "", s.end(state, sess, "", CodeInput)
	}
	if f.Dialect != "" {
		if f, err = withVariant(f, sess.Variant); err != nil {
			return "", s.end(state, sess, "", CodeInput)
		}
	}
	tokens, err := s.Tokens.Exchange(ctx, f, in.Code, sess.Redirect, sess.Verifier, state)
	if err != nil {
		code := string(ClassOf(err))
		if code == "" {
			code = string(ClassTransport)
		}
		return "", s.end(state, sess, "", code)
	}
	id, err := s.store(ctx, f, tokens, "")
	if err != nil {
		if s.Log != nil {
			s.Log.Warningf("oauth: storing a login failed: %v", err)
		}
		return "", s.end(state, sess, "", CodeStorage)
	}
	s.Sessions.End(state, id, "")
	s.release(sess)
	return id, nil
}

func (s *Service) end(state string, sess Session, id, code string) error {
	s.Sessions.End(state, id, code)
	s.release(sess)
	return &Failure{Code: code}
}

func (s *Service) release(sess Session) {
	if sess.Port != 0 && s.Listeners != nil {
		s.Listeners.Release(sess.Port)
	}
}

// store runs the login's hook and writes the account. deviceID, when the login
// made one, is kept in the account's provider data. A hook that fails ends the
// login: nothing is stored.
func (s *Service) store(ctx context.Context, f Flow, t Tokens, deviceID string) (string, error) {
	var acc Account
	if h, ok := s.dials().Hooks[f.Hook]; ok {
		var err error
		if acc, err = h(ctx, s.Tokens, f, t, callHeaders(f, deviceID)); err != nil {
			return "", err
		}
	}
	return s.save(ctx, f, t, acc, deviceID)
}

// save writes the account a login or a pasted token ends with.
func (s *Service) save(ctx context.Context, f Flow, t Tokens, acc Account, deviceID ...string) (string, error) {
	accounts := s.Accounts()
	if accounts == nil {
		return "", ErrStorage
	}
	now := s.Now()
	if acc.Email == "" {
		acc.Email = t.Email
	}
	fields := credentialFields(t, acc, now)
	if acc.ProjectID != "" {
		fields["projectId"] = document.Encode(acc.ProjectID)
	}
	fields["testStatus"] = document.Encode(repository.StatusActive)
	fields["lastRefreshAt"] = document.Encode(dialect.FormatTimestamp(now))
	psd := acc.PSD
	if psd == "" {
		psd = t.PSD
	}
	if len(deviceID) > 0 && deviceID[0] != "" {
		psd = withPSD(psd, deviceIDKey, deviceID[0])
	}
	if f.AppBase != "" {
		psd = withPSD(psd, "baseUrl", f.AppBase)
		psd = withPSD(psd, "clientId", f.ClientID)
		if f.ClientSecret != "" {
			psd = withPSD(psd, "clientSecret", f.ClientSecret)
		}
		psd = withPSD(psd, "authKind", "oauth")
	}
	conn, err := accounts.Create(ctx, repository.ConnInput{
		Provider: f.Provider, AuthType: "oauth", Email: acc.Email, Fields: fields, PSD: psd,
	}, nil)
	if err != nil {
		return "", err
	}
	return conn.ID, nil
}

// credentialFields turns tokens and what a hook decided into the stored fields.
func credentialFields(t Tokens, acc Account, now time.Time) map[string]string {
	access := t.Access
	if acc.Access != "" {
		access = acc.Access
	}
	fields := map[string]string{"accessToken": document.Encode(access)}
	if acc.APIKey != "" {
		fields["apiKey"] = document.Encode(acc.APIKey)
	}
	if t.ID != "" {
		fields["idToken"] = document.Encode(t.ID)
	}
	if t.Scope != "" {
		fields["scope"] = document.Encode(t.Scope)
	}
	if acc.NoRefresh {
		return fields
	}
	if t.Refresh != "" {
		fields["refreshToken"] = document.Encode(t.Refresh)
	}
	var expires time.Time
	if t.ExpiresIn > 0 {
		expires = now.Add(time.Duration(t.ExpiresIn) * time.Second)
		fields["expiresIn"] = strconv.FormatInt(t.ExpiresIn, 10)
	}
	if !acc.ExpiresAt.IsZero() && (expires.IsZero() || acc.ExpiresAt.Before(expires)) {
		expires = acc.ExpiresAt
	}
	if !expires.IsZero() {
		fields["expiresAt"] = document.Encode(dialect.FormatTimestamp(expires))
	}
	return fields
}

// withPSD sets one string key in a provider data object ("" is empty).
func withPSD(psd, k, v string) string {
	m := map[string]string{}
	if psd != "" {
		for key, val := range gjson.Parse(psd).Map() {
			if val.Type == gjson.String {
				m[key] = val.String()
			}
		}
	}
	m[k] = v
	return jsonObject(m)
}

// jsonObject renders a string map as a JSON object with sorted keys, or "".
func jsonObject(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []byte{'{'}
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, document.Encode(k)...)
		out = append(out, ':')
		out = append(out, document.Encode(m[k])...)
	}
	return string(append(out, '}'))
}

// dials returns the dialect table in force.
func (s *Service) dials() Dialects {
	if s.Dialects.empty() {
		return DefaultDialects()
	}
	return s.Dialects
}
