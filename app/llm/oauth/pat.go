package oauth

import (
	"context"
	"errors"

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
	u := f.Endpoints[upstream.EndpointUser]
	status, _, err := s.Tokens.Get(ctx, u, map[string]string{"Authorization": "Bearer " + token})
	if err != nil || status < 200 || status > 299 {
		return "", ErrToken
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
