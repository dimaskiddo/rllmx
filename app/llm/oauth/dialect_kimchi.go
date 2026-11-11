package oauth

import (
	"context"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// kimchiExchange is the browser sign-in whose callback carries the token
// itself: the provider's own page sends it to the redirect, so there is no code
// to trade and no secret to hold. What a login must still do is ask the service
// whether the token is one it will serve.
func kimchiExchange(ctx context.Context, c *TokenClient, f Flow, in CodeExchange) (Tokens, error) {
	if in.Code == "" {
		return Tokens{}, &Error{ClassMalformed}
	}
	u := f.Endpoints[upstream.EndpointValidate]
	if u == "" {
		return Tokens{}, &Error{ClassMalformed}
	}
	status, _, err := c.get(ctx, u, WithEntry(nil, "Authorization", "Bearer "+in.Code))
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, &Error{ClassRejected}
	}
	return Tokens{Access: in.Code}, nil
}

// kimchiNoRefresh reports that a Kimchi login is never renewed: the browser
// hands over a long-lived token, and an expired one is a new sign-in.
func kimchiNoRefresh(context.Context, *TokenClient, Flow, string, map[string]string) (Tokens, error) {
	return Tokens{}, &Error{ClassInvalidGrant}
}

// kimchiAccount reads the account the token belongs to. It is best effort: a
// login whose profile call fails still connects.
func kimchiAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	acc := Account{NoRefresh: true}
	u := f.Endpoints[upstream.EndpointUser]
	if u == "" || t.Access == "" {
		return acc, nil
	}
	status, raw, err := c.get(ctx, u, WithEntry(h, "Authorization", "Bearer "+t.Access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return acc, nil
	}
	acc.Email = CleanEmail(document.FirstString(gjson.GetBytes(raw, "email")))
	acc.PSD = kimchiPSD(raw)
	if acc.Email == "" {
		acc.Email = CleanEmail("kimchi-user-" + document.FirstString(gjson.GetBytes(raw, "id")))
	}
	return acc, nil
}

// kimchiPSD is the account data the profile names.
func kimchiPSD(raw []byte) string {
	psd := `{"authMethod":"browser_token"`
	if user := document.FirstString(gjson.GetBytes(raw, "username")); user != "" && len(user) <= maxProfileField {
		psd = withPSD(psd, "username", user)
	}
	if id := document.FirstString(gjson.GetBytes(raw, "id")); id != "" && len(id) <= maxProfileField {
		psd = withPSD(psd, "userId", id)
	}
	return psd + "}"
}
