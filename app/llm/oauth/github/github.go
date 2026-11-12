package github

import (
	"context"
	"rllmx/app/llm/oauth"
	"strconv"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// githubAccount mints the first Copilot token and reads the GitHub profile,
// both best effort: a login whose calls fail still connects, and the token is
// minted on the first request.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
	psd := map[string]string{}
	if f.Mint == upstream.MintCopilot {
		if token, expires, err := mint(ctx, c, &f.OAuth, t.Access); err == nil {
			psd[mintedField], psd[mintedUntil] = token, strconv.FormatInt(expires, 10)
		}
	}
	var acc oauth.Account
	if u := f.Endpoints[upstream.EndpointUser]; u != "" {
		status, raw, err := c.Get(ctx, u, map[string]string{"Authorization": "Bearer " + t.Access})
		if err == nil && status >= 200 && status < 300 && gjson.ValidBytes(raw) {
			acc.Email = oauth.ProfileText(raw, "email")
			oauth.SetText(psd, "githubLogin", oauth.ProfileText(raw, "login"))
			oauth.SetText(psd, "githubName", oauth.ProfileText(raw, "name"))
			oauth.SetText(psd, "githubEmail", acc.Email)
			if id := gjson.GetBytes(raw, "id"); id.Type == gjson.Number && id.Int() > 0 {
				psd["githubUserId"] = strconv.FormatInt(id.Int(), 10)
			}
		}
	}
	acc.PSD = oauth.JSONObject(psd)
	return acc, nil
}
