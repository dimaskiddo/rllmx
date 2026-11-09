package oauth

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/dialect"
	"rllmx/app/document"
	"rllmx/app/oidc"
	"rllmx/app/repository"
	"rllmx/app/upstream"
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
	Registry  *upstream.Registry
	ClientID  func(provider string) string
	Sessions  *Sessions
	Tokens    *TokenClient
	Accounts  func() Accounts
	Listeners *Listeners
	Log       log.Log
	Now       func() time.Time
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
	return FlowFor(s.Registry, s.ClientID, provider)
}

// Start opens a login. local says whether the browser runs on this machine;
// routerPort is the port the router listens on; bind ties the login to the
// dashboard session.
func (s *Service) Start(provider, bind string, local bool, routerPort int) (Started, error) {
	f, err := s.flow(provider)
	if err != nil {
		return Started{}, err
	}
	verifier, err := oidc.Verifier()
	if err != nil {
		return Started{}, err
	}
	redirect := f.RedirectURL(routerPort)
	port := f.ListenerPort()
	paste := !local
	if port != 0 {
		if s.Listeners == nil || !s.Listeners.Acquire(port, f.CallbackPath) {
			paste = true
		}
	}
	state, err := s.Sessions.Put(Session{Provider: f.Provider, Redirect: redirect, Verifier: verifier, Port: port, Bind: bind})
	if err != nil {
		if port != 0 && s.Listeners != nil {
			s.Listeners.Release(port)
		}
		return Started{}, err
	}
	return Started{AuthURL: f.URL(redirect, state, oidc.Challenge(verifier)), State: state, Redirect: redirect, Paste: paste}, nil
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
	f, err := s.flow(sess.Provider)
	if err != nil {
		return "", s.end(state, sess, "", CodeInput)
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
	accounts := s.Accounts()
	if accounts == nil {
		return "", ErrStorage
	}
	now := s.Now()
	var acc Account
	if h, ok := hooks[f.Hook]; ok {
		var err error
		if acc, err = h(ctx, s.Tokens, f, t, callHeaders(f, deviceID)); err != nil {
			return "", err
		}
	}
	if acc.Email == "" {
		acc.Email = t.Email
	}
	fields := credentialFields(t, acc, now)
	fields["testStatus"] = quote(repository.StatusActive)
	fields["lastRefreshAt"] = quote(dialect.FormatTimestamp(now))
	psd := acc.PSD
	if deviceID != "" {
		psd = withPSD(psd, deviceIDKey, deviceID)
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
	fields := map[string]string{"accessToken": quote(access)}
	if acc.APIKey != "" {
		fields["apiKey"] = quote(acc.APIKey)
	}
	if t.ID != "" {
		fields["idToken"] = quote(t.ID)
	}
	if t.Scope != "" {
		fields["scope"] = quote(t.Scope)
	}
	if acc.NoRefresh {
		return fields
	}
	if t.Refresh != "" {
		fields["refreshToken"] = quote(t.Refresh)
	}
	var expires time.Time
	if t.ExpiresIn > 0 {
		expires = now.Add(time.Duration(t.ExpiresIn) * time.Second)
		fields["expiresIn"] = jsonInt(t.ExpiresIn)
	}
	if !acc.ExpiresAt.IsZero() && (expires.IsZero() || acc.ExpiresAt.Before(expires)) {
		expires = acc.ExpiresAt
	}
	if !expires.IsZero() {
		fields["expiresAt"] = quote(dialect.FormatTimestamp(expires))
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

func quote(s string) string { return document.Encode(s) }

func jsonInt(n int64) string { return strconv.FormatInt(n, 10) }

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
		out = append(out, quote(k)...)
		out = append(out, ':')
		out = append(out, quote(m[k])...)
	}
	return string(append(out, '}'))
}
