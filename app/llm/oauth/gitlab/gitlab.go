package gitlab

import (
	"context"
	"rllmx/app/llm/oauth"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// gitlabAccount reads who the login belongs to from the user endpoint of the
// account's own server. It is best effort: a login whose call fails still
// connects, without a name.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
	u := f.Endpoints[upstream.EndpointUser]
	if u == "" {
		return oauth.Account{}, nil
	}
	status, raw, err := c.Get(ctx, u, map[string]string{"Authorization": "Bearer " + t.Access})
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return oauth.Account{}, nil
	}
	return gitlabProfile(raw), nil
}

// gitlabProfile reads the profile fields a GitLab account stores: the login
// name, the display name and the address (the public one when the main one is
// hidden).
func gitlabProfile(raw []byte) oauth.Account {
	acc := oauth.Account{Email: oauth.CleanEmail(document.FirstString(gjson.GetBytes(raw, "email"), gjson.GetBytes(raw, "public_email")))}
	psd := map[string]string{}
	oauth.SetText(psd, "username", oauth.ProfileText(raw, "username"))
	oauth.SetText(psd, "name", oauth.ProfileText(raw, "name"))
	oauth.SetText(psd, "email", acc.Email)
	acc.PSD = oauth.JSONObject(psd)
	return acc
}
