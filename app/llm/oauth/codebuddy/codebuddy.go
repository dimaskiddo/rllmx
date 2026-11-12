package codebuddy

import (
	"context"
	"net/http"
	"net/url"
	"rllmx/app/llm/oauth"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

const (
	codebuddyInterval = 5 * time.Second
	codebuddyPending  = 11217
	codebuddyLife     = 86400
)

// codebuddyStart asks the gateway for a login state and the page that approves
// it. The gateway has no client id and no device code: the state is both.
func Start(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, h map[string]string) (oauth.DeviceGrant, error) {
	target := f.DeviceURL + "?platform=" + url.QueryEscape(oauth.Param(f, "platform"))
	status, raw, err := c.PostJSON(ctx, target, "{}", oauth.WithEntries(h, map[string]string{
		"X-No-Authorization": "true", "X-No-User-Id": "true",
	}))
	if err != nil {
		return oauth.DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	body, ok := oauth.ObjectOf(raw)
	if !ok || body.Get("code").Int() != 0 {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	state := body.Get("data.state").String()
	page := body.Get("data.authUrl").String()
	if state == "" || len(state) > oauth.MaxDeviceCode || text.HasASCIIControl(state) || !oauth.PlainHTTPS(page) {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	return oauth.DeviceGrant{DeviceCode: state, VerificationURI: page, ExpiresIn: oauth.DefaultLife, Interval: codebuddyInterval}, nil
}

// codebuddyPoll asks for the tokens of a state. The gateway answers 200 with a
// code in the body: 0 with tokens, 11217 while the user has not approved.
func Poll(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, s oauth.DeviceState, h map[string]string) (oauth.Tokens, oauth.PollState, error) {
	status, raw, err := c.Get(ctx, f.TokenURL+"?state="+url.QueryEscape(s.Code), oauth.WithEntries(h, map[string]string{
		"X-No-Authorization": "true", "X-No-User-Id": "true", "X-No-Enterprise-Id": "true", "X-No-Department-Info": "true",
	}))
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, err
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	body, ok := oauth.ObjectOf(raw)
	if !ok {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassMalformed}
	}
	if body.Get("code").Int() == codebuddyPending {
		return oauth.Tokens{}, oauth.PollPending, nil
	}
	t, err := codebuddyTokens(body)
	return t, oauth.PollDone, err
}

// codebuddyTokens reads the camelCase token document of either call.
func codebuddyTokens(body gjson.Result) (oauth.Tokens, error) {
	if body.Get("code").Type != gjson.Number || body.Get("code").Int() != 0 {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassRejected}
	}
	t := oauth.Tokens{
		Access:    body.Get("data.accessToken").String(),
		Refresh:   body.Get("data.refreshToken").String(),
		ExpiresIn: body.Get("data.expiresIn").Int(),
	}
	if !oauth.BearerText(t.Access) || (t.Refresh != "" && !oauth.BearerText(t.Refresh)) {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = codebuddyLife
	}
	return t, nil
}

// codebuddyRefresh renews with the refresh token in a header and an empty body.
// Only a 401 means the sign-in is gone; a refusal in the body is not proof.
func Refresh(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, refreshToken string, h map[string]string) (oauth.Tokens, error) {
	status, raw, err := c.PostJSON(ctx, f.Refresh.URL, "{}", oauth.WithEntries(h, map[string]string{
		"X-Refresh-Token": refreshToken, "X-Auth-Refresh-Source": "plugin",
	}))
	if err != nil {
		return oauth.Tokens{}, err
	}
	switch {
	case status == http.StatusUnauthorized:
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassInvalidGrant}
	case status < 200 || status > 299:
		return oauth.Tokens{}, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	body, ok := oauth.ObjectOf(raw)
	if !ok {
		return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	return codebuddyTokens(body)
}
