package oauth

import (
	"context"
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
	// epochMillis tells a deadline given as a clock reading from a count of seconds.
	epochMillis = int64(1e12)
	// msInterval is the smallest number that must be milliseconds: no provider
	// asks for a pause of a minute or more between polls.
	msInterval = 60
)

// minimaxStart asks for a device code with a PKCE challenge. MiniMax may leave
// the device code out; then the user code is what the poll sends, and the login
// remembers that.
func minimaxStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	verifier, err := oidc.Verifier()
	if err != nil {
		return DeviceGrant{}, err
	}
	fields := map[string]string{
		"client_id": f.ClientID, "scope": f.Scope, "audience": paramOf(f, "audience"),
		"code_challenge": oidc.Challenge(verifier), "code_challenge_method": "S256",
	}
	status, raw, err := c.post(ctx, f.DeviceURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	body, ok := objectOf(raw)
	if !ok {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	g := DeviceGrant{
		DeviceCode: body.Get("device_code").String(), UserCode: body.Get("user_code").String(),
		VerificationURI: document.FirstString(body.Get("verification_uri_complete"), body.Get("verification_uri"), body.Get("verification_url")),
		Verifier:        verifier,
	}
	byUser := g.DeviceCode == ""
	if byUser {
		g.DeviceCode, g.Extra = g.UserCode, minimaxByUser
	}
	g.ExpiresIn = minimaxDeadline(body, c.now())
	g.Interval = minimaxInterval(body, byUser)
	if g.UserCode == "" || len(g.UserCode) > maxUserCode || len(g.DeviceCode) > maxDeviceCode || !plainHTTPS(g.VerificationURI) || text.HasASCIIControl(g.DeviceCode+g.UserCode) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	return g, nil
}

// minimaxDeadline reads the code's life: `expires_in` seconds, or `expired_in`
// as seconds or as a clock reading in milliseconds.
func minimaxDeadline(body gjson.Result, now time.Time) time.Duration {
	if n := body.Get("expires_in").Int(); n > 0 {
		return clampLife(n)
	}
	n := body.Get("expired_in").Int()
	switch {
	case n <= 0:
		return minimaxLife
	case n < epochMillis:
		return clampLife(n)
	}
	left := (time.UnixMilli(n).Sub(now)).Seconds()
	return clampLife(max(int64(left), 1))
}

// minimaxInterval reads the pause between polls: seconds on the usual shape,
// milliseconds on the one that polls with the user code.
func minimaxInterval(body gjson.Result, byUser bool) time.Duration {
	n := body.Get("interval").Int()
	if n <= 0 {
		return defaultInterval
	}
	if byUser && n >= msInterval {
		n = (n + 500) / 1000
	}
	return clampInterval(n)
}

// minimaxPoll asks for the tokens. The answer has its own `status` word on a
// 200, or a standard `error`.
func minimaxPoll(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
	fields := map[string]string{"grant_type": minimaxGrant, "client_id": f.ClientID, "code_verifier": s.Verifier}
	if s.Extra == minimaxByUser {
		fields["user_code"] = s.Code
	} else {
		fields["device_code"] = s.Code
	}
	status, raw, err := c.post(ctx, f.TokenURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return Tokens{}, pollDone, err
	}
	body, ok := objectOf(raw)
	if !ok {
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	word := body.Get("status").String()
	if word == "" {
		word = body.Get("error").String()
	}
	switch word {
	case "":
	case "pending", "authorization_pending":
		return Tokens{}, pollPending, nil
	case "slow_down":
		return Tokens{}, pollSlow, nil
	case "denied", "access_denied":
		return Tokens{}, pollDone, &Error{ClassDenied}
	case "expired", "expired_token":
		return Tokens{}, pollDone, &Error{ClassExpired}
	default:
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	if status < 200 || status > 299 {
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	t, err := parseTokens(raw)
	if err != nil {
		return Tokens{}, pollDone, err
	}
	if t.ExpiresIn <= 0 {
		t.ExpiresIn = minimaxTTL
	}
	return t, pollDone, nil
}
