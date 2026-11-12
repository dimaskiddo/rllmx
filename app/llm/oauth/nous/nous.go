package nous

import (
	"context"
	"rllmx/app/llm/oauth"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// nousAccount mints the agent key chat requests use. It is best effort: the
// access token works as the key when the mint fails.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	key, until, id := mintAgentKey(ctx, c, f, t.Access, h)
	if key == "" {
		return oauth.Account{}, nil
	}
	psd := map[string]string{}
	oauth.SetText(psd, "agentKeyId", id)
	if !until.IsZero() {
		psd["agentKeyExpiresAt"] = until.UTC().Format(time.RFC3339)
	}
	return oauth.Account{APIKey: key, ExpiresAt: until, PSD: oauth.JSONObject(psd)}, nil
}

// mintAgentKey asks for an agent key. It returns an empty key on any failure.
func mintAgentKey(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, access string, h map[string]string) (key string, until time.Time, id string) {
	target := f.Endpoints[upstream.EndpointAgentKey]
	if target == "" {
		return "", time.Time{}, ""
	}
	status, raw, err := c.PostJSON(ctx, target, `{"min_ttl_seconds":7200}`, oauth.WithEntry(h, "Authorization", "Bearer "+access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return "", time.Time{}, ""
	}
	key = gjson.GetBytes(raw, "api_key").String()
	if !oauth.CredentialText(key) {
		return "", time.Time{}, ""
	}
	return key, timeField(gjson.GetBytes(raw, "expires_at")), oauth.ProfileText(raw, "key_id")
}

// timeField reads a time given as an RFC 3339 string or Unix seconds, or zero.
func timeField(v gjson.Result) time.Time {
	switch v.Type {
	case gjson.String:
		if t, err := time.Parse(time.RFC3339, v.String()); err == nil {
			return t
		}
	case gjson.Number:
		if n := v.Int(); n > 0 {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}
