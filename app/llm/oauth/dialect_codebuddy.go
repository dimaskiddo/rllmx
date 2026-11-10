package oauth

import (
	"context"
	"net/http"
	"net/url"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

const (
	codebuddyInterval = 5 * time.Second
	codebuddyPending  = 11217
	codebuddyLife     = 86400
	maxBearer         = 16 << 10
)

// codebuddyStart asks the gateway for a login state and the page that approves
// it. The gateway has no client id and no device code: the state is both.
func codebuddyStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	target := f.DeviceURL + "?platform=" + url.QueryEscape(paramOf(f, "platform"))
	status, raw, err := c.postJSON(ctx, target, "{}", withEntries(h, map[string]string{
		"X-No-Authorization": "true", "X-No-User-Id": "true",
	}))
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	body, ok := objectOf(raw)
	if !ok || body.Get("code").Int() != 0 {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	state := body.Get("data.state").String()
	page := body.Get("data.authUrl").String()
	if state == "" || len(state) > maxDeviceCode || text.HasASCIIControl(state) || !plainHTTPS(page) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	return DeviceGrant{DeviceCode: state, VerificationURI: page, ExpiresIn: defaultLife, Interval: codebuddyInterval}, nil
}

// codebuddyPoll asks for the tokens of a state. The gateway answers 200 with a
// code in the body: 0 with tokens, 11217 while the user has not approved.
func codebuddyPoll(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
	status, raw, err := c.get(ctx, f.TokenURL+"?state="+url.QueryEscape(s.Code), withEntries(h, map[string]string{
		"X-No-Authorization": "true", "X-No-User-Id": "true", "X-No-Enterprise-Id": "true", "X-No-Department-Info": "true",
	}))
	if err != nil {
		return Tokens{}, pollDone, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	body, ok := objectOf(raw)
	if !ok {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	if body.Get("code").Int() == codebuddyPending {
		return Tokens{}, pollPending, nil
	}
	t, err := codebuddyTokens(body)
	return t, pollDone, err
}

// codebuddyTokens reads the camelCase token document of either call.
func codebuddyTokens(body gjson.Result) (Tokens, error) {
	if body.Get("code").Type != gjson.Number || body.Get("code").Int() != 0 {
		return Tokens{}, &Error{ClassRejected}
	}
	t := Tokens{
		Access:    body.Get("data.accessToken").String(),
		Refresh:   body.Get("data.refreshToken").String(),
		ExpiresIn: body.Get("data.expiresIn").Int(),
	}
	if !bearerText(t.Access) || (t.Refresh != "" && !bearerText(t.Refresh)) {
		return Tokens{}, &Error{ClassMalformed}
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = codebuddyLife
	}
	return t, nil
}

// codebuddyRefresh renews with the refresh token in a header and an empty body.
// Only a 401 means the sign-in is gone; a refusal in the body is not proof.
func codebuddyRefresh(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error) {
	status, raw, err := c.postJSON(ctx, f.Refresh.URL, "{}", withEntries(h, map[string]string{
		"X-Refresh-Token": refreshToken, "X-Auth-Refresh-Source": "plugin",
	}))
	if err != nil {
		return Tokens{}, err
	}
	switch {
	case status == http.StatusUnauthorized:
		return Tokens{}, &Error{ClassInvalidGrant}
	case status < 200 || status > 299:
		return Tokens{}, &Error{StatusClass(status)}
	}
	body, ok := objectOf(raw)
	if !ok {
		return Tokens{}, &Error{ClassMalformed}
	}
	return codebuddyTokens(body)
}
