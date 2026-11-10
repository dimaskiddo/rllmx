package oauth

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// freebuffDialect names the fingerprint device login: no client id, no secret,
// and a status call rather than a token call.
// freebuffInterval is how often the browser is asked whether it finished.
const freebuffInterval = 5 * time.Second

// freebuffStart opens a login: it hands the service a fingerprint of this
// login and gets back the address the user opens.
func freebuffStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	id, err := NewNonce()
	if err != nil {
		return DeviceGrant{}, &Error{ClassTransport}
	}
	status, raw, err := c.post(ctx, f.DeviceURL, upstream.EncodingJSON, map[string]string{"fingerprintId": id}, WithEntry(h, "User-Agent", freebuffUserAgent))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return DeviceGrant{}, &Error{ClassRejected}
	}
	loginURL := gjson.GetBytes(raw, "loginUrl").String()
	hash := gjson.GetBytes(raw, "fingerprintHash").String()
	expires := gjson.GetBytes(raw, "expiresAt").Int()
	if loginURL == "" || hash == "" || expires <= 0 || !plainHTTPS(loginURL) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	return DeviceGrant{
		DeviceCode:      id + "\x00" + hash + "\x00" + strconv.FormatInt(expires, 10),
		UserCode:        authCodeOf(loginURL),
		VerificationURI: loginURL,
		Interval:        freebuffInterval,
		ExpiresIn:       freebuffPollClamp,
	}, nil
}

// freebuffPoll reads the status call. 401 means the browser has not finished;
// a user object ends the login with its token.
func freebuffPoll(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
	id, hash, exp, ok := freebuffDevice(s.Code)
	if !ok {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	target := f.TokenURL + "?fingerprintId=" + url.QueryEscape(id) + "&fingerprintHash=" + url.QueryEscape(hash) + "&expiresAt=" + url.QueryEscape(exp)
	status, raw, err := c.get(ctx, target, WithEntry(h, "User-Agent", freebuffUserAgent))
	if err != nil {
		return Tokens{}, pollDone, err
	}
	if status == http.StatusUnauthorized || status == http.StatusNotFound {
		return Tokens{}, pollPending, nil
	}
	if status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return Tokens{}, pollDone, &Error{ClassRejected}
	}
	token := gjson.GetBytes(raw, "user.authToken").String()
	if token == "" {
		return Tokens{}, pollPending, nil
	}
	user := gjson.GetBytes(raw, "user")
	return Tokens{
		Access: token,
		Email:  CleanEmail(user.Get("email").String()),
		PSD:    `{"fingerprintId":` + document.Encode(id) + `,"userId":` + document.Encode(user.Get("id").String()) + `}`,
	}, pollDone, nil
}

// freebuffAccount is the login's own identity, already in the poll's answer.
func freebuffAccount(_ context.Context, _ *TokenClient, _ Flow, t Tokens, _ map[string]string) (Account, error) {
	return Account{Email: t.Email, PSD: t.PSD, NoRefresh: true}, nil
}

// freebuffDevice reads the three values the start call put in the grant.
func freebuffDevice(code string) (id, hash, exp string, ok bool) {
	parts := strings.Split(code, "\x00")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", false
	}
	return parts[0], parts[1], parts[2], true
}

// authCodeOf reads the login address's own code, for the dashboard to show.
func authCodeOf(loginURL string) string {
	u, err := url.Parse(loginURL)
	if err != nil {
		return ""
	}
	code := u.Query().Get("auth_code")
	if len(code) > 128 {
		return ""
	}
	return code
}
