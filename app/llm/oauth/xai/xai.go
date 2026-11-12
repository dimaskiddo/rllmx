package xai

import (
	"context"
	"rllmx/app/llm/oauth"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// grokAccount names the xAI account. The address comes from the id token, then
// the access token, then the Grok profile; a subject id is never taken for an
// address, because it is sent upstream in a header. The profile call is best
// effort.
func Grok(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	var acc oauth.Account
	for _, jwt := range []string{t.ID, t.Access} {
		if cl, ok := oauth.Claims(jwt); ok && acc.Email == "" {
			acc.Email = oauth.CleanEmail(document.FirstString(cl.Get("email"), cl.Get("preferred_username")))
		}
	}
	psd := map[string]string{}
	var access gjson.Result
	if u := f.Endpoints[upstream.EndpointUser]; u != "" {
		status, raw, err := c.Get(ctx, u, oauth.WithEntry(h, "Authorization", "Bearer "+t.Access))
		if err == nil && status >= 200 && status < 300 && gjson.ValidBytes(raw) {
			if acc.Email == "" {
				acc.Email = oauth.CleanEmail(oauth.ProfileText(raw, "email"))
			}
			id := oauth.ProfileText(raw, "userId")
			if id == "" {
				id = oauth.ProfileText(raw, "principalId")
			}
			oauth.SetText(psd, "userId", id)
			oauth.SetText(psd, "subscriptionTier", oauth.ProfileText(raw, "subscriptionTier"))
			access = gjson.GetBytes(raw, "hasGrokCodeAccess")
		}
	}
	oauth.SetText(psd, "email", acc.Email)
	acc.PSD = oauth.JSONObject(psd)
	if access.Type == gjson.True || access.Type == gjson.False {
		if acc.PSD == "" {
			acc.PSD = "{}"
		}
		if next, err := sjson.Set(acc.PSD, "hasGrokCodeAccess", access.Bool()); err == nil {
			acc.PSD = next
		}
	}
	return acc, nil
}

// xaiAccount names the xAI account from its id token, then its access token.
// A subject id is never taken for an address, because it can reach a header. It
// makes no call: the claims are all the login needs.
func Xai(_ context.Context, _ *oauth.TokenClient, _ oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
	var acc oauth.Account
	for _, jwt := range []string{t.ID, t.Access} {
		if cl, ok := oauth.Claims(jwt); ok && acc.Email == "" {
			acc.Email = oauth.CleanEmail(document.FirstString(cl.Get("email"), cl.Get("preferred_username")))
		}
	}
	return acc, nil
}
