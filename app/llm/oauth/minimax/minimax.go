package minimax

import (
	"context"
	"rllmx/app/llm/oauth"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/oidc"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

const (
	minimaxLife = 10 * time.Minute
	// minimaxGrant is the grant the poll asks with.
	minimaxGrant  = "urn:ietf:params:oauth:grant-type:device_code"
	minimaxTTL    = 3600
	minimaxByUser = "user"
	// msInterval is the smallest number that must be milliseconds: no provider
	// asks for a pause of a minute or more between polls.
	msInterval = 60
)

// minimaxStart asks for a device code with a PKCE challenge. MiniMax may leave
// the device code out; then the user code is what the poll sends, and the login
// remembers that.
func Start(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, h map[string]string) (oauth.DeviceGrant, error) {
	verifier, err := oidc.Verifier()
	if err != nil {
		return oauth.DeviceGrant{}, err
	}
	fields := map[string]string{
		"client_id": f.ClientID, "scope": f.Scope, "audience": oauth.Param(f, "audience"),
		"code_challenge": oidc.Challenge(verifier), "code_challenge_method": "S256",
	}
	status, raw, err := c.Post(ctx, f.DeviceURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return oauth.DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	body, ok := oauth.ObjectOf(raw)
	if !ok {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	g := oauth.DeviceGrant{
		DeviceCode: body.Get("device_code").String(), UserCode: body.Get("user_code").String(),
		VerificationURI: document.FirstString(body.Get("verification_uri_complete"), body.Get("verification_uri"), body.Get("verification_url")),
		Verifier:        verifier,
	}
	byUser := g.DeviceCode == ""
	if byUser {
		g.DeviceCode, g.Extra = g.UserCode, minimaxByUser
	}
	g.ExpiresIn = minimaxDeadline(body, c.Clock())
	g.Interval = minimaxInterval(body, byUser)
	if g.UserCode == "" || len(g.UserCode) > oauth.MaxUserCode || len(g.DeviceCode) > oauth.MaxDeviceCode || !oauth.PlainHTTPS(g.VerificationURI) || text.HasASCIIControl(g.DeviceCode+g.UserCode) {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	return g, nil
}

// minimaxDeadline reads the code's life: `expires_in` seconds, or `expired_in`
// as seconds or as a clock reading in milliseconds.
func minimaxDeadline(body gjson.Result, now time.Time) time.Duration {
	if n := body.Get("expires_in").Int(); n > 0 {
		return oauth.ClampLife(n)
	}
	n := body.Get("expired_in").Int()
	switch {
	case n <= 0:
		return minimaxLife
	case n < oauth.EpochMillis:
		return oauth.ClampLife(n)
	}
	left := (time.UnixMilli(n).Sub(now)).Seconds()
	return oauth.ClampLife(max(int64(left), 1))
}

// minimaxInterval reads the pause between polls: seconds on the usual shape,
// milliseconds on the one that polls with the user code.
func minimaxInterval(body gjson.Result, byUser bool) time.Duration {
	n := body.Get("interval").Int()
	if n <= 0 {
		return oauth.DefaultInterval
	}
	if byUser && n >= msInterval {
		n = (n + 500) / 1000
	}
	return oauth.ClampInterval(n)
}

// minimaxPoll asks for the tokens. The answer has its own `status` word on a
// 200, or a standard `error`.
func Poll(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, s oauth.DeviceState, h map[string]string) (oauth.Tokens, oauth.PollState, error) {
	fields := map[string]string{"grant_type": minimaxGrant, "client_id": f.ClientID, "code_verifier": s.Verifier}
	if s.Extra == minimaxByUser {
		fields["user_code"] = s.Code
	} else {
		fields["device_code"] = s.Code
	}
	status, raw, err := c.Post(ctx, f.TokenURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, err
	}
	body, ok := oauth.ObjectOf(raw)
	if !ok {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	word := body.Get("status").String()
	if word == "" {
		word = body.Get("error").String()
	}
	switch word {
	case "":
	case "pending", "authorization_pending":
		return oauth.Tokens{}, oauth.PollPending, nil
	case "slow_down":
		return oauth.Tokens{}, oauth.PollSlow, nil
	case "denied", "access_denied":
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassDenied}
	case "expired", "expired_token":
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassExpired}
	default:
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	if status < 200 || status > 299 {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	t, err := oauth.ParseTokens(raw)
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, err
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = minimaxTTL
	}
	return t, oauth.PollDone, nil
}
