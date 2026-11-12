package kilocode

import (
	"context"
	"net/http"
	"regexp"
	"rllmx/app/llm/oauth"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/text"
)

// kiloCode is the shape of a code that goes into a request path.
var kiloCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

const (
	kiloInterval = 3 * time.Second
	kiloLife     = 5 * time.Minute
)

// kiloStart asks for a code with an empty JSON body. The one code serves as the
// device code and the user code.
func Start(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, h map[string]string) (oauth.DeviceGrant, error) {
	status, raw, err := c.PostJSON(ctx, f.DeviceURL, "", h)
	if err != nil {
		return oauth.DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	code := gjson.GetBytes(raw, "code").String()
	page := gjson.GetBytes(raw, "verificationUrl").String()
	if !kiloCode.MatchString(code) || !oauth.PlainHTTPS(page) {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassMalformed}
	}
	life := kiloLife
	if n := gjson.GetBytes(raw, "expiresIn").Int(); n > 0 {
		life = oauth.ClampLife(n)
	}
	return oauth.DeviceGrant{DeviceCode: code, UserCode: code, VerificationURI: page, ExpiresIn: life, Interval: kiloInterval}, nil
}

// kiloPoll reads the answer by its HTTP status: 202 waits, 403 is a refusal, 410
// an expired code, and a 200 holds the token once the user approved.
func Poll(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, s oauth.DeviceState, h map[string]string) (oauth.Tokens, oauth.PollState, error) {
	return poll(ctx, c, f, s.Code, h)
}

// poll reads the answer for one code.
func poll(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, code string, h map[string]string) (oauth.Tokens, oauth.PollState, error) {
	if !kiloCode.MatchString(code) {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassMalformed}
	}
	status, raw, err := c.Get(ctx, strings.TrimRight(f.TokenURL, "/")+"/"+code, h)
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, err
	}
	switch {
	case status == http.StatusAccepted:
		return oauth.Tokens{}, oauth.PollPending, nil
	case status == http.StatusForbidden:
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassDenied}
	case status == http.StatusGone:
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassExpired}
	case status != http.StatusOK:
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassMalformed}
	}
	token := gjson.GetBytes(raw, "token").String()
	if gjson.GetBytes(raw, "status").String() != "approved" || token == "" {
		return oauth.Tokens{}, oauth.PollPending, nil
	}
	if len(token) > oauth.MaxDeviceCode || text.HasASCIIControl(token) {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassMalformed}
	}
	return oauth.Tokens{Access: token, Email: gjson.GetBytes(raw, "userEmail").String()}, oauth.PollDone, nil
}
