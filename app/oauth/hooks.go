package oauth

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/text"
	"rllmx/app/upstream"
)

// hook reads who a login belongs to from the tokens it just got, and may call
// the provider for more. h are the login's call headers for this account.
// Most hooks are best effort: what they cannot learn they leave out. A hook that
// returns an error ends the login, and the error carries a class, never text.
type hook func(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error)

// hooks are the login steps a data file may name. A test keeps the names equal
// to upstream.HookNames.
var hooks = map[string]hook{
	"codex": func(_ context.Context, _ *TokenClient, _ Flow, t Tokens, _ map[string]string) (Account, error) {
		return codexAccount(t), nil
	},
	"github":   githubAccount,
	"muse":     museAccount,
	"nous":     nousAccount,
	"kilocode": kiloAccount,
	"grokCli":  grokAccount,
}

const (
	maxProfileField = 256
	maxCredential   = 4096
	keyTries        = 3
	keyRetryBase    = 5 * time.Second
)

// githubAccount mints the first Copilot token and reads the GitHub profile,
// both best effort: a login whose calls fail still connects, and the token is
// minted on the first request.
func githubAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, _ map[string]string) (Account, error) {
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
	return acc, nil
}

// museAccount trades the Meta token for the key chat requests use. Without a
// key there is nothing to connect, so a failure here ends the login. The Meta
// token is not kept: nothing reads it again.
func museAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	target := f.Endpoints[upstream.EndpointKey]
	headers := withEntry(h, "Authorization", "Bearer "+t.Access)
	var raw []byte
	for try := range keyTries {
		if try > 0 {
			if err := c.wait(ctx, keyRetryBase<<(try-1)); err != nil {
				return Account{}, &Error{ClassTransport}
			}
		}
		status, body, err := c.postJSON(ctx, target, `{"onboard":true}`, headers)
		if err != nil {
			return Account{}, err
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			if try == keyTries-1 {
				return Account{}, &Error{ClassTransport}
			}
			continue
		}
		if status < 200 || status > 299 {
			return Account{}, &Error{ClassRejected}
		}
		raw = body
		break
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Account{}, &Error{ClassMalformed}
	}
	key := gjson.GetBytes(raw, "api_key").String()
	if gjson.GetBytes(raw, "is_subs_active").Type == gjson.False || !credentialText(key) {
		return Account{}, &Error{ClassRejected}
	}
	psd := map[string]string{}
	setText(psd, "subscriptionTier", profileText(raw, "subs_tier_name"))
	return Account{
		Email: strings.ToLower(profileText(raw, "user_email")), PSD: jsonObject(psd),
		Access: key, NoRefresh: true,
	}, nil
}

// nousAccount mints the agent key chat requests use. It is best effort: the
// access token works as the key when the mint fails.
func nousAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	key, until, id := mintAgentKey(ctx, c, f, t.Access, h)
	if key == "" {
		return Account{}, nil
	}
	psd := map[string]string{}
	setText(psd, "agentKeyId", id)
	if !until.IsZero() {
		psd["agentKeyExpiresAt"] = until.UTC().Format(time.RFC3339)
	}
	return Account{APIKey: key, ExpiresAt: until, PSD: jsonObject(psd)}, nil
}

// mintAgentKey asks for an agent key. It returns an empty key on any failure.
func mintAgentKey(ctx context.Context, c *TokenClient, f Flow, access string, h map[string]string) (key string, until time.Time, id string) {
	target := f.Endpoints[upstream.EndpointAgentKey]
	if target == "" {
		return "", time.Time{}, ""
	}
	status, raw, err := c.postJSON(ctx, target, `{"min_ttl_seconds":7200}`, withEntry(h, "Authorization", "Bearer "+access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return "", time.Time{}, ""
	}
	key = gjson.GetBytes(raw, "api_key").String()
	if !credentialText(key) {
		return "", time.Time{}, ""
	}
	return key, timeField(gjson.GetBytes(raw, "expires_at")), profileText(raw, "key_id")
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

// kiloAccount reads the organisation a Kilo Code request is billed to. It is
// best effort.
func kiloAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	u := f.Endpoints[upstream.EndpointProfile]
	if u == "" {
		return Account{}, nil
	}
	status, raw, err := c.get(ctx, u, withEntry(h, "Authorization", "Bearer "+t.Access))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return Account{}, nil
	}
	org := gjson.GetBytes(raw, "organizations.0.id")
	psd := map[string]string{}
	if org.Type == gjson.String && kiloCode.MatchString(org.String()) {
		psd["orgId"] = org.String()
	}
	return Account{PSD: jsonObject(psd)}, nil
}

// grokAccount names the xAI account. The address comes from the id token, then
// the access token, then the Grok profile; a subject id is never taken for an
// address, because it is sent upstream in a header. The profile call is best
// effort.
func grokAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	var acc Account
	for _, jwt := range []string{t.ID, t.Access} {
		if cl, ok := claims(jwt); ok && acc.Email == "" {
			acc.Email = cleanEmail(firstString(cl.Get("email"), cl.Get("preferred_username")))
		}
	}
	psd := map[string]string{}
	var access gjson.Result
	if u := f.Endpoints[upstream.EndpointUser]; u != "" {
		status, raw, err := c.get(ctx, u, withEntry(h, "Authorization", "Bearer "+t.Access))
		if err == nil && status >= 200 && status < 300 && gjson.ValidBytes(raw) {
			if acc.Email == "" {
				acc.Email = cleanEmail(profileText(raw, "email"))
			}
			id := profileText(raw, "userId")
			if id == "" {
				id = profileText(raw, "principalId")
			}
			setText(psd, "userId", id)
			setText(psd, "subscriptionTier", profileText(raw, "subscriptionTier"))
			access = gjson.GetBytes(raw, "hasGrokCodeAccess")
		}
	}
	setText(psd, "email", acc.Email)
	acc.PSD = jsonObject(psd)
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

// cleanEmail returns an address that is short and free of control characters,
// else "".
func cleanEmail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxProfileField || text.HasASCIIControl(s) || strings.ContainsAny(s, " \t") {
		return ""
	}
	return s
}

// credentialText reports a value fit to be stored and sent as a credential.
func credentialText(s string) bool {
	return s != "" && len(s) <= maxCredential && !text.HasASCIIControl(s)
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
