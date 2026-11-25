package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"rllmx/app/llm/qoder"
	"rllmx/app/llm/upstream"
)

// ErrToken means a pasted token was refused by the server it was meant for.
var ErrToken = errors.New("oauth: the server did not accept that token")

// ConnectToken stores a personal access token as an account. The token is
// checked first against the user endpoint of the server it names, presented the
// way chat will present it, so a token the check accepts is one a request can
// use. Such an account has no refresh token and no expiry.
func (s *Service) ConnectToken(ctx context.Context, provider, baseURL, token string) (string, error) {
	f, err := s.flow(provider)
	if err != nil {
		return "", err
	}
	if !f.PAT {
		return "", ErrUnsupported
	}
	if !upstream.BareOrigin(baseURL) || !CredentialText(token) {
		return "", ErrToken
	}
	f.Endpoints = rebaseAll(f.Endpoints, baseURL)
	if provider == "qoder" || provider == "qoder-cn" {
		// A Qoder token is a personal token: the exchange and a quota read are
		// the proof that a request can use it.
		if err := s.checkQoderToken(ctx, baseURL, token); err != nil {
			return "", ErrToken
		}
	} else {
		u := f.Endpoints[upstream.EndpointUser]
		status, _, err := s.Tokens.Get(ctx, u, map[string]string{"Authorization": "Bearer " + token})
		if err != nil || status < 200 || status > 299 {
			return "", ErrToken
		}
	}
	acc := Account{}
	if h := s.dials().Hooks[f.Hook]; h != nil {
		if got, err := h(ctx, s.Tokens, f, Tokens{Access: token}, CallHeaders(f, "")); err == nil {
			acc = got
		}
	}
	acc.PSD = WithPSD(acc.PSD, "baseUrl", baseURL)
	acc.PSD = WithPSD(acc.PSD, "authKind", "personal_access_token")
	id, err := s.Save(ctx, f, Tokens{Access: token}, Account{Email: acc.Email, PSD: acc.PSD, NoRefresh: true})
	if err != nil {
		if s.Log != nil {
			s.Log.Warningf("oauth: storing a token failed: %v", err)
		}
		return "", ErrStorage
	}
	return id, nil
}

// checkQoderToken trades the personal token for a job token and reads the
// account's quota with it, which is what a request will present.
func (s *Service) checkQoderToken(ctx context.Context, baseURL, token string) error {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return ErrToken
	}
	base := "https://" + u.Host
	job, _, err := qoder.Exchange(ctx, func(ctx context.Context, method, target string, body []byte, headers map[string]string) (int, []byte, error) {
		return s.Tokens.Send(ctx, method, target, body, headers)
	}, base, token)
	if err != nil {
		return err
	}
	status, _, err := s.Tokens.Get(ctx, base+"/api/v2/quota/usage", map[string]string{"Authorization": "Bearer " + job})
	if err != nil || status < http.StatusOK || status > 299 {
		return ErrToken
	}
	return nil
}
