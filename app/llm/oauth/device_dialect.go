package oauth

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

// A dialect is a device login that does not follow RFC 8628. It replaces the
// start and the poll; the rest of the login (the session, the interval, the
// hook, the storage) is the same engine.
type DeviceDialect struct {
	start func(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error)
	poll  func(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error)
	// refresh replaces the standard renewal when the provider has its own.
	refresh func(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error)
}

// DeviceState is what a poll needs from the server-side login: the device code
// and what the dialect's start kept for it. None of it reaches the browser.
type DeviceState struct {
	Code     string
	Verifier string
	Extra    string
}

// deviceDialects are the names a data file may use for it. A test keeps the table
// equal to upstream.DialectNames.

// kiloCode is the shape of a code that goes into a request path.
var kiloCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	kiloInterval = 3 * time.Second
	kiloLife     = 5 * time.Minute
)

// kiloStart asks for a code with an empty JSON body. The one code serves as the
// device code and the user code.
func kiloStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	status, raw, err := c.postJSON(ctx, f.DeviceURL, "", h)
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	code := gjson.GetBytes(raw, "code").String()
	page := gjson.GetBytes(raw, "verificationUrl").String()
	if !kiloCode.MatchString(code) || !plainHTTPS(page) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	life := kiloLife
	if n := gjson.GetBytes(raw, "expiresIn").Int(); n > 0 {
		life = clampLife(n)
	}
	return DeviceGrant{DeviceCode: code, UserCode: code, VerificationURI: page, ExpiresIn: life, Interval: kiloInterval}, nil
}

// kiloPoll reads the answer by its HTTP status: 202 waits, 403 is a refusal, 410
// an expired code, and a 200 holds the token once the user approved.
func kiloPoll(ctx context.Context, c *TokenClient, f Flow, code string, h map[string]string) (Tokens, PollState, error) {
	if !kiloCode.MatchString(code) {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	status, raw, err := c.get(ctx, strings.TrimRight(f.TokenURL, "/")+"/"+code, h)
	if err != nil {
		return Tokens{}, pollDone, err
	}
	switch {
	case status == http.StatusAccepted:
		return Tokens{}, pollPending, nil
	case status == http.StatusForbidden:
		return Tokens{}, pollDone, &Error{ClassDenied}
	case status == http.StatusGone:
		return Tokens{}, pollDone, &Error{ClassExpired}
	case status != http.StatusOK:
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	token := gjson.GetBytes(raw, "token").String()
	if gjson.GetBytes(raw, "status").String() != "approved" || token == "" {
		return Tokens{}, pollPending, nil
	}
	if len(token) > maxDeviceCode || text.HasASCIIControl(token) {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	return Tokens{Access: token, Email: gjson.GetBytes(raw, "userEmail").String()}, pollDone, nil
}
