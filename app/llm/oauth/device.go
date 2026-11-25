package oauth

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
)

// Bounds on what a provider's device answer may hold. The page the user is sent
// to is shown as a link, so it must be a plain https address.
const (
	DefaultInterval = 5 * time.Second
	minInterval     = time.Second
	maxInterval     = time.Minute
	slowStep        = 5 * time.Second
	DefaultLife     = 15 * time.Minute
	maxLife         = 30 * time.Minute
	MaxDeviceCode   = 1024
	MaxUserCode     = 64
	maxVerifyURL    = 512
	// EpochMillis tells a deadline given as a clock reading from a count of seconds.
	EpochMillis = int64(1e12)
)

// DeviceGrant is what a provider hands out to start a device login.
type DeviceGrant struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       time.Duration
	Interval        time.Duration
	// Verifier and Extra are kept with the login for the poll: a PKCE verifier,
	// and whatever else the dialect needs to remember.
	Verifier string
	Extra    string
}

// PollState says what an answer that carries no tokens means.
type PollState int

const (
	PollDone PollState = iota
	PollPending
	PollSlow
)

// Device asks the provider for a device code. deviceID fills the placeholder in
// the data's call headers.
func (c *TokenClient) Device(ctx context.Context, f Flow, deviceID string) (DeviceGrant, error) {
	h := CallHeaders(f, deviceID)
	if d, ok := c.dials().Device[f.Dialect]; ok {
		return d.Start(ctx, c, f, h)
	}
	fields := map[string]string{}
	if f.Scope != "" {
		fields["scope"] = f.Scope
	}
	if !f.NoClient {
		fields["client_id"] = f.ClientID
	}
	for _, p := range f.Params {
		fields[p.Name] = p.Value
	}
	status, raw, err := c.Post(ctx, f.DeviceURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	g, err := ParseGrant(raw, f.Endpoints[upstream.EndpointVerify])
	if err != nil {
		return DeviceGrant{}, err
	}
	return g, nil
}

func StatusClass(status int) Class {
	if status >= 500 {
		return ClassTransport
	}
	return ClassRejected
}

// ParseGrant reads a device answer. fallback is the page used when the answer
// names none; with a user code it carries the code as a query parameter.
func ParseGrant(raw []byte, fallback string) (DeviceGrant, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	g := DeviceGrant{
		DeviceCode: gjson.GetBytes(raw, "device_code").String(),
		UserCode:   gjson.GetBytes(raw, "user_code").String(),
		ExpiresIn:  ClampLife(gjson.GetBytes(raw, "expires_in").Int()),
		Interval:   ClampInterval(gjson.GetBytes(raw, "interval").Int()),
	}
	for _, key := range []string{"verification_uri_complete", "verification_uri"} {
		if v := gjson.GetBytes(raw, key).String(); v != "" {
			g.VerificationURI = v
			break
		}
	}
	if g.VerificationURI == "" && fallback != "" {
		g.VerificationURI = fallback
		if g.UserCode != "" && !text.HasASCIIControl(g.UserCode) && !strings.ContainsAny(g.UserCode, " #&?=") {
			g.VerificationURI += "?user_code=" + url.QueryEscape(g.UserCode)
		}
	}
	if g.DeviceCode == "" || len(g.DeviceCode) > MaxDeviceCode || text.HasASCIIControl(g.DeviceCode) ||
		len(g.UserCode) > MaxUserCode || text.HasASCIIControl(g.UserCode) || !PlainHTTPS(g.VerificationURI) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	return g, nil
}

// PlainHTTPS reports an https address with a host and no credentials.
func PlainHTTPS(s string) bool {
	if s == "" || len(s) > maxVerifyURL || text.HasASCIIControl(s) || strings.ContainsAny(s, " \\") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func ClampLife(seconds int64) time.Duration {
	if seconds <= 0 {
		return DefaultLife
	}
	return time.Duration(min(seconds, int64(maxLife/time.Second))) * time.Second
}

func ClampInterval(seconds int64) time.Duration {
	if seconds <= 0 {
		return DefaultInterval
	}
	return max(minInterval, time.Duration(min(seconds, int64(maxInterval/time.Second)))*time.Second)
}

// DevicePoll asks once whether the user has approved the code. A provider that
// has not heard from the user yet answers with an error name in a 200 body, as
// the specification allows, so the body decides before the status does.
func (c *TokenClient) DevicePoll(ctx context.Context, f Flow, st DeviceState, deviceID string) (Tokens, PollState, error) {
	h := CallHeaders(f, deviceID)
	if d, ok := c.dials().Device[f.Dialect]; ok {
		return d.Poll(ctx, c, f, st, h)
	}
	fields := map[string]string{"device_code": st.Code, "grant_type": "urn:ietf:params:oauth:grant-type:device_code"}
	if !f.NoClient {
		fields["client_id"] = f.ClientID
	}
	status, raw, err := c.Post(ctx, f.TokenURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return Tokens{}, PollDone, err
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Tokens{}, PollDone, &Error{StatusClass(status)}
	}
	switch gjson.GetBytes(raw, "error").String() {
	case "":
	case "authorization_pending":
		return Tokens{}, PollPending, nil
	case "slow_down":
		return Tokens{}, PollSlow, nil
	case "expired_token":
		return Tokens{}, PollDone, &Error{ClassExpired}
	case "access_denied":
		return Tokens{}, PollDone, &Error{ClassDenied}
	default:
		return Tokens{}, PollDone, &Error{StatusClass(status)}
	}
	if status < 200 || status > 299 {
		return Tokens{}, PollDone, &Error{StatusClass(status)}
	}
	t, err := ParseTokens(raw)
	return t, PollDone, err
}
