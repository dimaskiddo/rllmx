package muse

import (
	"context"
	"net/http"
	"rllmx/app/llm/oauth"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// keyTries and keyRetryBase bound the retries of the one call a login cannot
// connect without.
const (
	keyTries     = 3
	keyRetryBase = 5 * time.Second
)

// museAccount trades the Meta token for the key chat requests use. Without a
// key there is nothing to connect, so a failure here ends the login. The Meta
// token is not kept: nothing reads it again.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	target := f.Endpoints[upstream.EndpointKey]
	headers := oauth.WithEntry(h, "Authorization", "Bearer "+t.Access)
	var raw []byte
	for try := range keyTries {
		if try > 0 {
			if err := c.Wait(ctx, keyRetryBase<<(try-1)); err != nil {
				return oauth.Account{}, &oauth.Error{Class: oauth.ClassTransport}
			}
		}
		status, body, err := c.PostJSON(ctx, target, `{"onboard":true}`, headers)
		if err != nil {
			return oauth.Account{}, err
		}
		if status == http.StatusTooManyRequests || status >= 500 {
			if try == keyTries-1 {
				return oauth.Account{}, &oauth.Error{Class: oauth.ClassTransport}
			}
			continue
		}
		if status < 200 || status > 299 {
			return oauth.Account{}, &oauth.Error{Class: oauth.ClassRejected}
		}
		raw = body
		break
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return oauth.Account{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	key := gjson.GetBytes(raw, "api_key").String()
	if gjson.GetBytes(raw, "is_subs_active").Type == gjson.False || !oauth.CredentialText(key) {
		return oauth.Account{}, &oauth.Error{Class: oauth.ClassRejected}
	}
	psd := map[string]string{}
	oauth.SetText(psd, "subscriptionTier", oauth.ProfileText(raw, "subs_tier_name"))
	return oauth.Account{
		Email: strings.ToLower(oauth.ProfileText(raw, "user_email")), PSD: oauth.JSONObject(psd),
		Access: key, NoRefresh: true,
	}, nil
}
