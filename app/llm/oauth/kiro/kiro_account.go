package kiro

import (
	"context"
	"rllmx/app/llm/oauth"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// kiroAccount reads the account's own CodeWhisperer profile, which a request
// must name, and the address its token belongs to. Both are best effort: a
// login that cannot list its profiles still connects, and the request then
// carries no profile at all.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	acc := oauth.Account{Email: tokenEmail(t.Access)}
	psd := t.PSD
	arn := kiroProfile(ctx, c, f, t.Access, h)
	if arn == "" {
		arn = gjson.Get(psd, "profileArn").String()
	}
	if arn != "" && prefixOK(arn, "arn:aws:codewhisperer:") {
		psd, _ = sjson.Set(psd, "profileArn", arn)
	}
	psd, _ = sjson.Set(psd, "authMethod", "builder-id")
	psd, _ = sjson.Set(psd, "region", kiroRegionOf(f))
	acc.PSD = psd
	return acc, nil
}

// kiroProfile asks for the profiles the token may use and takes the one in the
// login's own region, so a token bound to another account's shared profile is
// never sent.
func kiroProfile(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, access string, h map[string]string) string {
	base := f.Endpoints[upstream.EndpointProfiles]
	if base == "" || access == "" {
		return ""
	}
	headers := oauth.WithEntry(h, "Content-Type", "application/x-amz-json-1.0")
	headers = oauth.WithEntry(headers, "x-amz-target", "AmazonCodeWhispererService.ListAvailableProfiles")
	headers = oauth.WithEntry(headers, "Accept", "application/json")
	headers = oauth.WithEntry(headers, "Authorization", "Bearer "+access)
	status, raw, err := c.PostJSON(ctx, strings.TrimRight(base, "/"), `{"maxResults":10}`, headers)
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return ""
	}
	region := kiroRegionOf(f)
	var first string
	for _, p := range gjson.GetBytes(raw, "profiles").Array() {
		arn := p.Get("arn").String()
		if arn == "" {
			arn = p.Get("profileArn").String()
		}
		if arn == "" {
			continue
		}
		if first == "" {
			first = arn
		}
		// The ARN's fourth field is the region it was made in.
		if parts := strings.Split(arn, ":"); len(parts) > 3 && parts[3] == region {
			return arn
		}
	}
	return first
}

// tokenEmail reads the address a token states about itself. A token that is not
// one this program can read leaves the account unnamed.
func tokenEmail(token string) string {
	c, ok := oauth.Claims(token)
	if !ok {
		return ""
	}
	return oauth.CleanEmail(document.FirstString(c.Get("email"), c.Get("preferred_username")))
}

// prefixOK reports a string that starts with prefix and holds nothing that
// could change the shape of a header or a body.
func prefixOK(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) < 512 &&
		!strings.ContainsAny(value, " \t\r\n\"\\")
}

// kiroRegionOf is the region every Kiro address is built for: the one the
// provider's data names, since the login has no account to read one from.
func kiroRegionOf(oauth.Flow) string { return "us-east-1" }
