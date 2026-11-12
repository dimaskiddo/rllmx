package kimchi

import (
	"context"
	"rllmx/app/llm/oauth"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// kimchiExchange is the browser sign-in whose callback carries the token
// itself: the provider's own page sends it to the redirect, so there is no code
// to trade and no secret to hold. What a login must still do is ask the service
// whether the token is one it will serve.
func Exchange(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, in oauth.CodeExchange) (oauth.Tokens, error) {
	if in.Code == "" {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	u := f.Endpoints[upstream.EndpointValidate]
	if u == "" {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	status, _, err := c.Get(ctx, u, oauth.WithEntry(nil, "Authorization", "Bearer "+in.Code))
	if err != nil {
		return oauth.Tokens{}, err
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassRejected}
	}
	return oauth.Tokens{Access: in.Code}, nil
}

// kimchiNoRefresh reports that a Kimchi login is never renewed: the browser
// hands over a long-lived token, and an expired one is a new sign-in.
func NoRefresh(context.Context, *oauth.TokenClient, oauth.Flow, string, map[string]string) (oauth.Tokens, error) {
	return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassInvalidGrant}
}

// kimchiAccount reads the account the token belongs to. It is best effort: a
// login whose profile call fails still connects.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	acc := oauth.Account{NoRefresh: true}
	u := f.Endpoints[upstream.EndpointUser]
	if u == "" || t.Access == "" {
		return acc, nil
	}
	status, raw, err := c.Get(ctx, u, oauth.WithEntry(h, "Authorization", "Bearer "+t.Access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return acc, nil
	}
	acc.Email = oauth.CleanEmail(document.FirstString(gjson.GetBytes(raw, "email")))
	acc.PSD = kimchiPSD(raw)
	if acc.Email == "" {
		acc.Email = oauth.CleanEmail("kimchi-user-" + document.FirstString(gjson.GetBytes(raw, "id")))
	}
	return acc, nil
}

// kimchiPSD is the account data the profile names.
func kimchiPSD(raw []byte) string {
	psd := `{"authMethod":"browser_token"`
	if user := document.FirstString(gjson.GetBytes(raw, "username")); user != "" && len(user) <= oauth.MaxProfileField {
		psd = oauth.WithPSD(psd, "username", user)
	}
	if id := document.FirstString(gjson.GetBytes(raw, "id")); id != "" && len(id) <= oauth.MaxProfileField {
		psd = oauth.WithPSD(psd, "userId", id)
	}
	return psd + "}"
}
