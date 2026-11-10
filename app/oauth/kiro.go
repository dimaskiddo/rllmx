package oauth

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/upstream"
)

// kiroAccount reads the account's own CodeWhisperer profile, which a request
// must name, and the address its token belongs to. Both are best effort: a
// login that cannot list its profiles still connects, and the request then
// carries no profile at all.
func kiroAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	acc := Account{Email: tokenEmail(t.Access)}
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
func kiroProfile(ctx context.Context, c *TokenClient, f Flow, access string, h map[string]string) string {
	base := f.Endpoints[upstream.EndpointProfiles]
	if base == "" || access == "" {
		return ""
	}
	headers := withEntry(h, "Content-Type", "application/x-amz-json-1.0")
	headers = withEntry(headers, "x-amz-target", "AmazonCodeWhispererService.ListAvailableProfiles")
	headers = withEntry(headers, "Accept", "application/json")
	headers = withEntry(headers, "Authorization", "Bearer "+access)
	status, raw, err := c.postJSON(ctx, strings.TrimRight(base, "/"), `{"maxResults":10}`, headers)
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
	c, ok := claims(token)
	if !ok {
		return ""
	}
	return cleanEmail(firstString(c.Get("email"), c.Get("preferred_username")))
}

// prefixOK reports a string that starts with prefix and holds nothing that
// could change the shape of a header or a body.
func prefixOK(value, prefix string) bool {
	return strings.HasPrefix(value, prefix) && len(value) < 512 &&
		!strings.ContainsAny(value, " \t\r\n\"\\")
}

// kiroRegionOf is the region every Kiro address is built for: the one the
// provider's data names, since the login has no account to read one from.
func kiroRegionOf(Flow) string { return "us-east-1" }
