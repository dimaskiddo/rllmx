package oauth

import (
	"context"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
)

// Kiro's social sign-in is Google or GitHub through AWS Cognito. The provider
// whitelists only its own custom scheme for the redirect, so nothing on a server
// receives the answer: the browser lands on `kiro://…` and the user pastes that
// address back, which ParseInput already reads. The session, the state, the
// paste box and the storage are the engine every other code login uses.
//
// kiroSocialRedirect is the address the provider answers. It follows the scheme
// the provider registered rather than this router's own callback, so it is a
// constant and not flow data: the exchange must repeat it byte for byte.
const kiroSocialRedirect = "kiro://kiro.kiroAgent/authenticate-success"

// kiroIdpParam names the identity provider in the flow's own parameters.
const kiroIdpParam = "idp"

// kiroSocialAuthorize is the address to open. The provider takes the identity
// provider, the redirect this router cannot change, a PKCE challenge and an
// account chooser.
func kiroSocialAuthorize(f Flow, state, challenge string) string {
	pairs := [][2]string{
		{"idp", paramOf(f, kiroIdpParam)},
		{"redirect_uri", kiroSocialRedirect},
		{"code_challenge", challenge},
		{"code_challenge_method", "S256"},
		{"state", state},
		{"prompt", "select_account"},
	}
	var b strings.Builder
	b.WriteString(f.AuthorizeURL)
	for i, p := range pairs {
		if i == 0 {
			b.WriteByte('?')
		} else {
			b.WriteByte('&')
		}
		b.WriteString(p[0])
		b.WriteByte('=')
		b.WriteString(escapeQuery(p[1]))
	}
	return b.String()
}

// kiroSocialExchange trades a pasted code for tokens. The verifier comes from
// the login's own session, and the redirect must be the one the authorize call
// sent, so neither is read from the pasted text.
func kiroSocialExchange(ctx context.Context, c *TokenClient, f Flow, in codeInput) (Tokens, error) {
	status, raw, err := c.postJSON(ctx, f.TokenURL,
		jsonObject(map[string]string{"code": in.Code, "code_verifier": in.Verifier, "redirect_uri": kiroSocialRedirect}), nil)
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, &Error{statusClass(status)}
	}
	return kiroSocialTokens(raw, "")
}

// kiroSocialRefresh renews a social sign-in with the body this provider
// registered.
func kiroSocialRefresh(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error) {
	status, raw, err := c.postJSON(ctx, f.TokenURL, jsonObject(map[string]string{"refreshToken": refreshToken}), h)
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, &Error{statusClass(status)}
	}
	return kiroSocialTokens(raw, refreshToken)
}

// kiroSocialTokens reads a sign-in answer: camelCase names, and the profile the
// account signs in with. keep is the refresh token to retain when the provider
// does not rotate one.
func kiroSocialTokens(raw []byte, keep string) (Tokens, error) {
	a, ok := objectOf(raw)
	if !ok {
		return Tokens{}, &Error{ClassMalformed}
	}
	t := Tokens{Access: a.Get("accessToken").String(), Refresh: a.Get("refreshToken").String()}
	if !bearerText(t.Access) || (t.Refresh != "" && !bearerText(t.Refresh)) {
		return Tokens{}, &Error{ClassMalformed}
	}
	if t.Refresh == "" {
		t.Refresh = keep
	}
	if n := a.Get("expiresIn").Int(); n > 0 {
		t.ExpiresIn = n
	}
	t.PSD = jsonObject(map[string]string{"profileArn": a.Get("profileArn").String()})
	return t, nil
}

// kiroSocialAccount reads who the login belongs to and which identity provider
// it used. A login whose token names no address still connects.
func kiroSocialAccount(_ context.Context, _ *TokenClient, f Flow, t Tokens, _ map[string]string) (Account, error) {
	return Account{
		Email: tokenEmail(t.Access),
		PSD: jsonObject(map[string]string{
			"profileArn": gjson.Get(t.PSD, "profileArn").String(),
			"authMethod": paramOf(f, kiroIdpParam),
			"region":     kiroRegionOf(f),
		}),
	}, nil
}

// escapeQuery writes a query value with a space as %20, which is what the
// provider's own clients send.
func escapeQuery(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// The identity providers a Kiro social login can use.
const (
	kiroIDPGoogle = "google"
	kiroIDPGitHub = "github"
)

// withVariant picks which of a provider's several logins this is. Only a
// provider whose data offers a choice of identity provider takes one, so no
// other login can be steered by the request.
func withVariant(f Flow, variant string) (Flow, error) {
	if f.Dialect != kiroSocialDialect {
		return f, nil
	}
	if variant != kiroIDPGoogle && variant != kiroIDPGitHub {
		return Flow{}, ErrVariant
	}
	f.Params = append(append([]upstream.Param{}, f.Params...), upstream.Param{Name: kiroIdpParam, Value: variant})
	return f, nil
}

// kiroSocialDialect is the dialect name the data file uses.
const kiroSocialDialect = "kiro-social"
