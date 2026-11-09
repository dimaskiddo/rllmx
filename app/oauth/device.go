package oauth

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/text"
	"rllmx/app/upstream"
)

// Bounds on what a provider's device answer may hold. The page the user is sent
// to is shown as a link, so it must be a plain https address.
const (
	defaultInterval = 5 * time.Second
	minInterval     = time.Second
	maxInterval     = time.Minute
	slowStep        = 5 * time.Second
	defaultLife     = 15 * time.Minute
	maxLife         = 30 * time.Minute
	maxDeviceCode   = 1024
	maxUserCode     = 64
	maxVerifyURL    = 512
)

// DeviceGrant is what a provider hands out to start a device login.
type DeviceGrant struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       time.Duration
	Interval        time.Duration
}

// pollState says what an answer that carries no tokens means.
type pollState int

const (
	pollDone pollState = iota
	pollPending
	pollSlow
)

// Device asks the provider for a device code. deviceID fills the placeholder in
// the data's call headers.
func (c *TokenClient) Device(ctx context.Context, f Flow, deviceID string) (DeviceGrant, error) {
	h := callHeaders(f, deviceID)
	if d, ok := deviceDialects[f.Dialect]; ok {
		return d.start(ctx, c, f, h)
	}
	fields := map[string]string{"scope": f.Scope}
	if !f.NoClient {
		fields["client_id"] = f.ClientID
	}
	for _, p := range f.Params {
		fields[p.Name] = p.Value
	}
	status, raw, err := c.post(ctx, f.DeviceURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 {
		return DeviceGrant{}, &Error{statusClass(status)}
	}
	g, err := parseGrant(raw, f.Endpoints[upstream.EndpointVerify])
	if err != nil {
		return DeviceGrant{}, err
	}
	return g, nil
}

func statusClass(status int) Class {
	if status >= 500 {
		return ClassTransport
	}
	return ClassRejected
}

// parseGrant reads a device answer. fallback is the page used when the answer
// names none; with a user code it carries the code as a query parameter.
func parseGrant(raw []byte, fallback string) (DeviceGrant, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	g := DeviceGrant{
		DeviceCode: gjson.GetBytes(raw, "device_code").String(),
		UserCode:   gjson.GetBytes(raw, "user_code").String(),
		ExpiresIn:  clampLife(gjson.GetBytes(raw, "expires_in").Int()),
		Interval:   clampInterval(gjson.GetBytes(raw, "interval").Int()),
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
	if g.DeviceCode == "" || len(g.DeviceCode) > maxDeviceCode || text.HasASCIIControl(g.DeviceCode) ||
		len(g.UserCode) > maxUserCode || text.HasASCIIControl(g.UserCode) || !plainHTTPS(g.VerificationURI) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	return g, nil
}

// plainHTTPS reports an https address with a host and no credentials.
func plainHTTPS(s string) bool {
	if s == "" || len(s) > maxVerifyURL || text.HasASCIIControl(s) || strings.ContainsAny(s, " \\") {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func clampLife(seconds int64) time.Duration {
	if seconds <= 0 {
		return defaultLife
	}
	return min(time.Duration(seconds)*time.Second, maxLife)
}

func clampInterval(seconds int64) time.Duration {
	if seconds <= 0 {
		return defaultInterval
	}
	return max(minInterval, min(time.Duration(seconds)*time.Second, maxInterval))
}

// DevicePoll asks once whether the user has approved the code. A provider that
// has not heard from the user yet answers with an error name in a 200 body, as
// the specification allows, so the body decides before the status does.
func (c *TokenClient) DevicePoll(ctx context.Context, f Flow, deviceCode, deviceID string) (Tokens, pollState, error) {
	h := callHeaders(f, deviceID)
	if d, ok := deviceDialects[f.Dialect]; ok {
		return d.poll(ctx, c, f, deviceCode, h)
	}
	fields := map[string]string{"device_code": deviceCode, "grant_type": "urn:ietf:params:oauth:grant-type:device_code"}
	if !f.NoClient {
		fields["client_id"] = f.ClientID
	}
	status, raw, err := c.post(ctx, f.TokenURL, upstream.EncodingForm, fields, h)
	if err != nil {
		return Tokens{}, pollDone, err
	}
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Tokens{}, pollDone, &Error{statusClass(status)}
	}
	switch gjson.GetBytes(raw, "error").String() {
	case "":
	case "authorization_pending":
		return Tokens{}, pollPending, nil
	case "slow_down":
		return Tokens{}, pollSlow, nil
	case "expired_token":
		return Tokens{}, pollDone, &Error{ClassExpired}
	case "access_denied":
		return Tokens{}, pollDone, &Error{ClassDenied}
	default:
		return Tokens{}, pollDone, &Error{statusClass(status)}
	}
	if status < 200 || status > 299 {
		return Tokens{}, pollDone, &Error{statusClass(status)}
	}
	t, err := parseTokens(raw)
	return t, pollDone, err
}
