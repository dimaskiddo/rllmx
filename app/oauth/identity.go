package oauth

import (
	"encoding/base64"
	"strings"

	"github.com/tidwall/gjson"
)

// Account is who a login belongs to, read from the tokens.
type Account struct {
	Email string
	// PSD is a providerSpecificData object as JSON, or "".
	PSD string
}

const maxJWT = 16 << 10

// claims decodes the payload of a JWT without checking its signature: the
// claims only name the account and decide duplicates, they grant nothing.
func claims(jwt string) (gjson.Result, bool) {
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

// codexAccount reads the email, the ChatGPT account id and the plan from the
// ID token, then the email alone from the access token.
func codexAccount(t Tokens) Account {
	var acc Account
	var id, plan string
	if c, ok := claims(t.ID); ok {
		acc.Email = c.Get("email").String()
		id = firstString(c.Get(`https://api\.openai\.com/auth.chatgpt_account_id`), c.Get("account_id"))
		plan = firstString(c.Get(`https://api\.openai\.com/auth.chatgpt_plan_type`), c.Get("plan_type"))
	}
	if acc.Email == "" {
		if c, ok := claims(t.Access); ok {
			acc.Email = firstString(c.Get("email"), c.Get("preferred_username"), c.Get("sub"))
		}
	}
	psd := map[string]string{}
	if id != "" {
		psd["chatgptAccountId"] = id
	}
	if plan != "" {
		psd["chatgptPlanType"] = plan
	}
	acc.PSD = jsonObject(psd)
	return acc
}

func firstString(vals ...gjson.Result) string {
	for _, v := range vals {
		if s := v.String(); s != "" && v.Type == gjson.String {
			return s
		}
	}
	return ""
}
