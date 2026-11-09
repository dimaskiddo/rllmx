package oauth

import (
	"context"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/text"
	"rllmx/app/upstream"
)

// hook reads who a login belongs to from the tokens it just got, and may call
// the provider for more. It never fails the login: what it cannot learn it
// leaves out.
type hook func(ctx context.Context, c *TokenClient, f Flow, t Tokens) Account

// hooks are the login steps a data file may name. A test keeps the names equal
// to upstream.HookNames.
var hooks = map[string]hook{
	"codex":  func(_ context.Context, _ *TokenClient, _ Flow, t Tokens) Account { return codexAccount(t) },
	"github": githubAccount,
}

const maxProfileField = 256

// githubAccount mints the first Copilot token and reads the GitHub profile,
// both best effort: a login whose calls fail still connects, and the token is
// minted on the first request.
func githubAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens) Account {
	psd := map[string]string{}
	if f.Mint == upstream.MintCopilot {
		if token, expires, err := mint(ctx, c, &f.OAuth, t.Access); err == nil {
			psd[mintedField], psd[mintedUntil] = token, strconv.FormatInt(expires, 10)
		}
	}
	var acc Account
	if u := f.Endpoints[upstream.EndpointUser]; u != "" {
		status, raw, err := c.get(ctx, u, map[string]string{"Authorization": "Bearer " + t.Access})
		if err == nil && status >= 200 && status < 300 && gjson.ValidBytes(raw) {
			acc.Email = profileText(raw, "email")
			setText(psd, "githubLogin", profileText(raw, "login"))
			setText(psd, "githubName", profileText(raw, "name"))
			setText(psd, "githubEmail", acc.Email)
			if id := gjson.GetBytes(raw, "id"); id.Type == gjson.Number && id.Int() > 0 {
				psd["githubUserId"] = strconv.FormatInt(id.Int(), 10)
			}
		}
	}
	acc.PSD = jsonObject(psd)
	return acc
}

// profileText reads a short text field of a profile, or "" for anything else.
func profileText(raw []byte, key string) string {
	v := gjson.GetBytes(raw, key)
	s := strings.TrimSpace(v.String())
	if v.Type != gjson.String || len(s) > maxProfileField || text.HasASCIIControl(s) {
		return ""
	}
	return s
}

func setText(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}
