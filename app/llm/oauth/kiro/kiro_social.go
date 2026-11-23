package kiro

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
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
func SocialAuthorize(f oauth.Flow, state, challenge string) string {
	pairs := [][2]string{
		{"idp", oauth.Param(f, kiroIdpParam)},
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
func SocialExchange(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, in oauth.CodeExchange) (oauth.Tokens, error) {
	status, raw, err := c.PostJSON(ctx, f.TokenURL,
		oauth.JSONObject(map[string]string{"code": in.Code, "code_verifier": in.Verifier, "redirect_uri": kiroSocialRedirect}), nil)
	if err != nil {
		return oauth.Tokens{}, err
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	return kiroSocialTokens(raw, "")
}

// kiroSocialRefresh renews a social sign-in with the body this provider
// registered.
func SocialRefresh(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, refreshToken string, h map[string]string) (oauth.Tokens, error) {
	status, raw, err := c.PostJSON(ctx, f.TokenURL, oauth.JSONObject(map[string]string{"refreshToken": refreshToken}), h)
	if err != nil {
		return oauth.Tokens{}, err
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	return kiroSocialTokens(raw, refreshToken)
}

// kiroSocialTokens reads a sign-in answer: camelCase names, and the profile the
// account signs in with. keep is the refresh token to retain when the provider
// does not rotate one.
func kiroSocialTokens(raw []byte, keep string) (oauth.Tokens, error) {
	a, ok := oauth.ObjectOf(raw)
	if !ok {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	t := oauth.Tokens{Access: a.Get("accessToken").String(), Refresh: a.Get("refreshToken").String()}
	if !oauth.BearerText(t.Access) || (t.Refresh != "" && !oauth.BearerText(t.Refresh)) {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	if t.Refresh == "" {
		t.Refresh = keep
	}
	if n := a.Get("expiresIn").Int(); n > 0 {
		t.ExpiresIn = n
	}
	t.PSD = oauth.JSONObject(map[string]string{"profileArn": a.Get("profileArn").String()})
	return t, nil
}

// kiroSocialAccount reads who the login belongs to and which identity provider
// it used. A login whose token names no address still connects.
func SocialAccount(_ context.Context, _ *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
	return oauth.Account{
		Email: tokenEmail(t.Access),
		PSD: oauth.JSONObject(map[string]string{
			"profileArn": gjson.Get(t.PSD, "profileArn").String(),
			"authMethod": oauth.Param(f, kiroIdpParam),
			"region":     kiroRegionOf(f),
		}),
	}, nil
}

// escapeQuery writes a query value with a space as %20, which is what the
// provider's own clients send.
func escapeQuery(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// Variants are the identity providers a Kiro social login can use. The
// dashboard offers exactly these, and Variant accepts exactly these, so a name
// added here is offered and accepted together.
func Variants() []string {
	return []string{upstream.VariantGoogle, upstream.VariantGitHub}
}

// Variant picks which of a provider's several logins this is. Only a
// provider whose data offers a choice of identity provider takes one, so no
// other login can be steered by the request.
func Variant(f oauth.Flow, variant string) (oauth.Flow, error) {
	if f.Dialect != kiroSocialDialect {
		return f, nil
	}
	if !slices.Contains(Variants(), variant) {
		return oauth.Flow{}, oauth.ErrVariant
	}
	f.Params = append(append([]upstream.Param{}, f.Params...), upstream.Param{Name: kiroIdpParam, Value: variant})
	return f, nil
}

// kiroSocialDialect is the dialect name the data file uses.
const kiroSocialDialect = "kiro-social"
