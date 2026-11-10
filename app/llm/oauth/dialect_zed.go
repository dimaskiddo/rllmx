package oauth

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/llm/zed"
	"rllmx/app/store/document"
)

// zedDialect is the native-app sign-in: no client id, no secret, but a key pair
// made for this login whose private half reads the answer.
const zedDialect = "zed"

// zedSignInKey is where the login's own key is kept: the session's verifier
// slot, which the engine never sends to the browser. The sign-in's system id
// rides beside it, separated by a dot.
const zedSignInKey = "zed-rsa-pkcs8:"

// zedAuthorize makes a key pair for this login and returns the address it signs
// in at. The key is returned with it, because the engine holds it with the
// session: it never reaches the browser.
func zedAuthorize(f Flow) (string, string, error) {
	signIn, addr, err := zed.NewSignIn(f.FixedPort)
	if err != nil {
		return "", "", err
	}
	encoded, err := signIn.Encode()
	if err != nil {
		return "", "", err
	}
	return addr, zedSignInKey + encoded + "." + signIn.SystemID, nil
}

// zedExchange reads the answer the browser brought back with the login's own
// private key.
func zedExchange(ctx context.Context, c *TokenClient, f Flow, in CodeExchange) (Tokens, error) {
	signIn, err := zedKey(in.Verifier)
	if err != nil {
		return Tokens{}, &Error{ClassMalformed}
	}
	cb, err := zed.ParseCallback(in.Code)
	if err != nil {
		return Tokens{}, &Error{ClassMalformed}
	}
	token, err := signIn.Decrypt(cb.Encrypted)
	if err != nil {
		return Tokens{}, &Error{ClassMalformed}
	}
	return Tokens{Access: token, UserID: cb.UserID, SystemID: signIn.SystemID}, nil
}

// zedKey reads the login's own key out of the verifier the engine kept.
func zedKey(verifier string) (zed.SignIn, error) {
	if !strings.HasPrefix(verifier, zedSignInKey) {
		return zed.SignIn{}, zed.ErrBadKey
	}
	body := strings.TrimPrefix(verifier, zedSignInKey)
	key, system, ok := strings.Cut(body, ".")
	if !ok {
		return zed.SignIn{}, zed.ErrBadKey
	}
	return zed.DecodeSignIn(key, system)
}

// zedNoRefresh reports that a Zed login is never renewed: the account's own
// token is long lived and the hosted one is minted from it.
func zedNoRefresh(context.Context, *TokenClient, Flow, string, map[string]string) (Tokens, error) {
	return Tokens{}, &Error{ClassInvalidGrant}
}

// zedAccount reads the account's own identity and organization from the
// service. Both steps are best effort: a login whose calls fail still connects.
func zedAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	acc := Account{PSD: `{"userId":` + document.Encode(t.UserID) + `,"systemId":` + document.Encode(t.SystemID) + `}`}
	u := f.Endpoints[upstream.EndpointUser]
	if u == "" || t.Access == "" {
		return acc, nil
	}
	psd := `{"userId":` + document.Encode(t.UserID) + `,"systemId":` + document.Encode(t.SystemID)
	status, raw, err := c.get(ctx, u+"/client/users/me", WithEntry(h, "Authorization", zed.UserAuth(zed.Account{Access: t.Access, UserID: t.UserID})))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		acc.PSD = psd + `}`
		return acc, nil
	}
	if email := zedEmail(raw); email != "" {
		acc.Email = email
	}
	if org := zedOrganization(raw); org != "" {
		psd += `,"organizationId":` + document.Encode(org)
	}
	acc.PSD = psd + `}`
	return acc, nil
}

// zedEmail is the address the service reports, or "".
func zedEmail(raw []byte) string {
	for _, p := range []string{"email", "user.email"} {
		if v := gjson.GetBytes(raw, p).String(); v != "" {
			return CleanEmail(v)
		}
	}
	return ""
}

// zedOrganization is the account's default organization, else its personal one,
// else the first.
func zedOrganization(raw []byte) string {
	for _, p := range []string{"default_organization_id", "defaultOrganizationId"} {
		if v := gjson.GetBytes(raw, p).String(); v != "" {
			return v
		}
	}
	orgs := gjson.GetBytes(raw, "organizations")
	for _, o := range orgs.Array() {
		if o.Get("is_personal").Bool() {
			return o.Get("id").String()
		}
	}
	return orgs.Get("0.id").String()
}
