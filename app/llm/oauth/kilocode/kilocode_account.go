package kilocode

import (
	"context"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
)

// Account reads the organisation a Kilo Code request is billed to. It is best
// effort.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	u := f.Endpoints[upstream.EndpointProfile]
	if u == "" {
		return oauth.Account{}, nil
	}
	status, raw, err := c.Get(ctx, u, oauth.WithEntry(h, "Authorization", "Bearer "+t.Access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return oauth.Account{}, nil
	}
	org := gjson.GetBytes(raw, "organizations.0.id")
	psd := map[string]string{}
	if org.Type == gjson.String && kiloCode.MatchString(org.String()) {
		psd["orgId"] = org.String()
	}
	return oauth.Account{PSD: oauth.JSONObject(psd)}, nil
}
