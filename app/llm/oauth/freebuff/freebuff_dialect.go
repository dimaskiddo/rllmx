package freebuff

import (
	"context"
	"net/http"
	"net/url"
	"rllmx/app/llm/oauth"
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
func Start(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, h map[string]string) (oauth.DeviceGrant, error) {
	id, err := oauth.NewNonce()
	if err != nil {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassTransport}
	}
	status, raw, err := c.Post(ctx, f.DeviceURL, upstream.EncodingJSON, map[string]string{"fingerprintId": id}, oauth.WithEntry(h, "User-Agent", freebuffUserAgent))
	if err != nil || status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassRejected}
	}
	loginURL := gjson.GetBytes(raw, "loginUrl").String()
	hash := gjson.GetBytes(raw, "fingerprintHash").String()
	expires := gjson.GetBytes(raw, "expiresAt").Int()
	if loginURL == "" || hash == "" || expires <= 0 || !oauth.PlainHTTPS(loginURL) {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	return oauth.DeviceGrant{
		DeviceCode:      id + "\x00" + hash + "\x00" + strconv.FormatInt(expires, 10),
		UserCode:        authCodeOf(loginURL),
		VerificationURI: loginURL,
		Interval:        freebuffInterval,
		ExpiresIn:       freebuffPollClamp,
	}, nil
}

// freebuffPoll reads the status call. 401 means the browser has not finished;
// a user object ends the login with its token.
func Poll(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, s oauth.DeviceState, h map[string]string) (oauth.Tokens, oauth.PollState, error) {
	id, hash, exp, ok := freebuffDevice(s.Code)
	if !ok {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassMalformed}
	}
	target := f.TokenURL + "?fingerprintId=" + url.QueryEscape(id) + "&fingerprintHash=" + url.QueryEscape(hash) + "&expiresAt=" + url.QueryEscape(exp)
	status, raw, err := c.Get(ctx, target, oauth.WithEntry(h, "User-Agent", freebuffUserAgent))
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, err
	}
	if status == http.StatusUnauthorized || status == http.StatusNotFound {
		return oauth.Tokens{}, oauth.PollPending, nil
	}
	if status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassRejected}
	}
	token := gjson.GetBytes(raw, "user.authToken").String()
	if token == "" {
		return oauth.Tokens{}, oauth.PollPending, nil
	}
	user := gjson.GetBytes(raw, "user")
	return oauth.Tokens{
		Access: token,
		Email:  oauth.CleanEmail(user.Get("email").String()),
		PSD:    `{"fingerprintId":` + document.Encode(id) + `,"userId":` + document.Encode(user.Get("id").String()) + `}`,
	}, oauth.PollDone, nil
}

// freebuffAccount is the login's own identity, already in the poll's answer.
func Account(_ context.Context, _ *oauth.TokenClient, _ oauth.Flow, t oauth.Tokens, _ map[string]string) (oauth.Account, error) {
	return oauth.Account{Email: t.Email, PSD: t.PSD, NoRefresh: true}, nil
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
