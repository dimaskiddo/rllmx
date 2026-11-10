package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net/mail"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// A code dialect is a code login whose exchange and renewal are not the plain
// token calls: the code may carry the tokens itself, and the renewal has its
// own body and envelope. The rest of the login (the session, the state, the
// hook, the storage) is the same engine.
type codeDialect struct {
	exchange func(ctx context.Context, c *TokenClient, f Flow, in codeInput) (Tokens, error)
	refresh  func(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error)
}

// codeDialects are the names a data file may use for it. A test keeps the table
// equal to upstream.CodeDialectNames.
var codeDialects = map[string]codeDialect{
	"cline":       {exchange: clineExchange, refresh: clineRefresh},
	"kiro-social": {exchange: kiroSocialExchange, refresh: kiroSocialRefresh},
}

// codeInput is what a code login's exchange works from: the code the browser
// brought back, the verifier the login made, and the redirect it sent.
type codeInput struct {
	Code     string
	Verifier string
	Redirect string
}

// newNonce is 16 random bytes in hex, for a provider that wants a nonce.
func newNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

const (
	maxClineCode = 16 << 10
	// clineClient is what the provider calls the kind of client that logs in.
	clineClient = "extension"
)

// clineExchange takes the tokens out of the code itself. The provider hands the
// browser a base64 JSON document that holds them; when the code is not that, it
// is an ordinary code and the token address trades it.
func clineExchange(ctx context.Context, c *TokenClient, f Flow, in codeInput) (Tokens, error) {
	if t, ok := clineTokens(in.Code, c.now()); ok {
		return t, nil
	}
	status, raw, err := c.postJSON(ctx, f.TokenURL, jsonBody(map[string]string{
		"grant_type": "authorization_code", "code": in.Code, "client_type": clineClient, "redirect_uri": in.Redirect,
	}), nil)
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, &Error{classify(raw)}
	}
	return clineAnswer(raw, "", c.now())
}

// clineTokens reads the tokens a code holds. It accepts base64 in either
// alphabet with or without padding, and cuts the text after the last closing
// brace, as the provider pads its document. It reports false for anything that
// is not a document with an access token.
func clineTokens(code string, now time.Time) (Tokens, bool) {
	if code == "" || len(code) > maxClineCode {
		return Tokens{}, false
	}
	s := strings.NewReplacer("-", "+", "_", "/").Replace(strings.TrimRight(code, "="))
	raw, err := base64.RawStdEncoding.DecodeString(s)
	if err != nil {
		return Tokens{}, false
	}
	end := bytes.LastIndexByte(raw, '}')
	if end < 0 {
		return Tokens{}, false
	}
	raw = raw[:end+1]
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Tokens{}, false
	}
	t, err := clineFields(gjson.ParseBytes(raw), "", now)
	return t, err == nil
}

// clineRefresh renews with the provider's own body and reads its envelope.
func clineRefresh(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error) {
	status, raw, err := c.postJSON(ctx, f.Refresh.URL, jsonBody(map[string]string{
		"refreshToken": refreshToken, "grantType": "refresh_token", "clientType": clineClient,
	}), h)
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, &Error{classify(raw)}
	}
	return clineAnswer(raw, refreshToken, c.now())
}

// clineAnswer reads a token answer, which may sit inside a `data` envelope.
func clineAnswer(raw []byte, keep string, now time.Time) (Tokens, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Tokens{}, &Error{ClassMalformed}
	}
	body := gjson.ParseBytes(raw)
	if d := body.Get("data"); d.IsObject() {
		body = d
	}
	return clineFields(body, keep, now)
}

// clineFields reads the fields of a token document: camelCase names, an ISO
// expiry, and the address under `email` or `userInfo.email`.
func clineFields(body gjson.Result, keep string, now time.Time) (Tokens, error) {
	t := Tokens{
		Access:  body.Get("accessToken").String(),
		Refresh: body.Get("refreshToken").String(),
		Email:   cleanAddress(firstString(body.Get("email"), body.Get("userInfo.email"))),
	}
	if !credentialText(t.Access) || (t.Refresh != "" && !credentialText(t.Refresh)) {
		return Tokens{}, &Error{ClassMalformed}
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
	if s == "" || len(s) > maxProfileField {
		return ""
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Name != "" {
		return ""
	}
	return a.Address
}

// jsonBody renders a flat string map as a JSON object.
func jsonBody(m map[string]string) string { return jsonObject(m) }
