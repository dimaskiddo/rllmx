package cline

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/mail"
	"rllmx/app/llm/oauth"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/store/document"
)

const (
	maxClineCode = 16 << 10
	// clineClient is what the provider calls the kind of client that logs in.
	clineClient = "extension"
)

// clineExchange takes the tokens out of the code itself. The provider hands the
// browser a base64 JSON document that holds them; when the code is not that, it
// is an ordinary code and the token address trades it.
func Exchange(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, in oauth.CodeExchange) (oauth.Tokens, error) {
	if t, ok := clineTokens(in.Code, c.Clock()); ok {
		return t, nil
	}
	status, raw, err := c.PostJSON(ctx, f.TokenURL, oauth.JSONObject(map[string]string{
		"grant_type": "authorization_code", "code": in.Code, "client_type": clineClient, "redirect_uri": in.Redirect,
	}), nil)
	if err != nil {
		return oauth.Tokens{}, err
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.Classify(raw)}
	}
	return clineAnswer(raw, "", c.Clock())
}

// clineTokens reads the tokens a code holds. It accepts base64 in either
// alphabet with or without padding, and cuts the text after the last closing
// brace, as the provider pads its document. It reports false for anything that
// is not a document with an access token.
func clineTokens(code string, now time.Time) (oauth.Tokens, bool) {
	if code == "" || len(code) > maxClineCode {
		return oauth.Tokens{}, false
	}
	s := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimRight(code, "="))
	raw, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return oauth.Tokens{}, false
	}
	end := bytes.LastIndexByte(raw, '}')
	if end < 0 {
		return oauth.Tokens{}, false
	}
	raw = raw[:end+1]
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return oauth.Tokens{}, false
	}
	t, err := clineFields(gjson.ParseBytes(raw), "", now)
	return t, err == nil
}

// clineRefresh renews with the provider's own body and reads its envelope.
func Refresh(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, refreshToken string, h map[string]string) (oauth.Tokens, error) {
	status, raw, err := c.PostJSON(ctx, f.Refresh.URL, oauth.JSONObject(map[string]string{
		"refreshToken": refreshToken, "grantType": "refresh_token", "clientType": clineClient,
	}), h)
	if err != nil {
		return oauth.Tokens{}, err
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.Classify(raw)}
	}
	return clineAnswer(raw, refreshToken, c.Clock())
}

// clineAnswer reads a token answer, which may sit inside a `data` envelope.
func clineAnswer(raw []byte, keep string, now time.Time) (oauth.Tokens, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	body := gjson.ParseBytes(raw)
	if d := body.Get("data"); d.IsObject() {
		body = d
	}
	return clineFields(body, keep, now)
}

// clineFields reads the fields of a token document: camelCase names, an ISO
// expiry, and the address under `email` or `userInfo.email`.
func clineFields(body gjson.Result, keep string, now time.Time) (oauth.Tokens, error) {
	t := oauth.Tokens{
		Access:  body.Get("accessToken").String(),
		Refresh: body.Get("refreshToken").String(),
		Email:   cleanAddress(document.FirstString(body.Get("email"), body.Get("userInfo.email"))),
	}
	if !oauth.CredentialText(t.Access) || (t.Refresh != "" && !oauth.CredentialText(t.Refresh)) {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	if t.Refresh == "" {
		t.Refresh = keep
	}
	if exp, err := time.Parse(time.RFC3339, body.Get("expiresAt").String()); err == nil {
		t.ExpiresIn = max(int64(exp.Sub(now).Seconds()), 1)
	}
	return t, nil
}

// cleanAddress is an email address that parses, or "".
func cleanAddress(s string) string {
	if s == "" || len(s) > oauth.MaxProfileField {
		return ""
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" {
		return ""
	}
	return a.Address
}
