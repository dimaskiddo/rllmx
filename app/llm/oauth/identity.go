package oauth

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/store/document"
)

// Account is who a login belongs to, read from the tokens, and what a hook
// changes about the credential that is stored.
type Account struct {
	Email string
	// PSD is a providerSpecificData object as JSON, or "".
	PSD string
	// ProjectID is the Cloud Code project of a Google account; it is stored as the
	// connection's own `projectId` field.
	ProjectID string
	// Access replaces the access token that is stored, for a login whose real
	// credential is minted from the token the provider handed out.
	Access string
	// APIKey is stored as the key a request carries; it wins over the access
	// token.
	APIKey string
	// NoRefresh drops the refresh token and the expiry, for a credential that
	// cannot be renewed.
	NoRefresh bool
	// ExpiresAt, when it falls before the token's own expiry, is stored instead.
	ExpiresAt time.Time
}

const maxJWT = 16 << 10

// Claims decodes the payload of a JWT without checking its signature: the
// Claims only name the account and decide duplicates, they grant nothing.
func Claims(jwt string) (gjson.Result, bool) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || len(jwt) > maxJWT {
		return gjson.Result{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil || !gjson.ValidBytes(raw) {
		return gjson.Result{}, false
	}
	r := gjson.ParseBytes(raw)
	return r, r.IsObject()
}

// claimsAccount reads the email and the ChatGPT account from the ID token, then
// from the access token whatever the first did not name: a pasted access token
// carries the same claims and has no ID token beside it. It makes no call, so a
// pasted list can use it too.
func claimsAccount(t Tokens) Account {
	var acc Account
	var id, plan string
	for _, tok := range []string{t.ID, t.Access} {
		c, ok := Claims(tok)
		if !ok {
			continue
		}
		if acc.Email == "" {
			acc.Email = document.FirstString(c.Get("email"), c.Get(`https://api\.openai\.com/profile.email`), c.Get("preferred_username"))
		}
		if id == "" {
			id = document.FirstString(c.Get(`https://api\.openai\.com/auth.chatgpt_account_id`), c.Get("account_id"))
		}
		if plan == "" {
			plan = document.FirstString(c.Get(`https://api\.openai\.com/auth.chatgpt_plan_type`), c.Get("plan_type"))
		}
	}
	psd := map[string]string{}
	if id != "" {
		psd["chatgptAccountId"] = id
	}
	if plan != "" {
		psd["chatgptPlanType"] = plan
	}
	acc.PSD = JSONObject(psd)
	return acc
}
