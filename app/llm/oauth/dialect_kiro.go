package oauth

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/store/document"
)

// Kiro's sign-in is AWS SSO OIDC: an OIDC client is registered for this login,
// that client asks for a device code, and the poll trades the code for the
// account's token. The registered client pair belongs to the login and goes
// with it, because a renewal needs the same pair.
const (
	kiroClientName = "kiro-oauth-client"
	kiroClientType = "public"
	kiroIssueURL   = "https://identitycenter.amazonaws.com/ssoins-722374e8c3c8e6c6"
	kiroStartURL   = "https://view.awsapps.com/start"
	// kiroScopes are the three the IDE's client asks for.
	kiroScopes = "codewhisperer:completions codewhisperer:analysis codewhisperer:conversations"
)

// kiroRegisterPath and kiroAuthPath are the two calls a start makes, on the
// same host as the token endpoint.
const (
	kiroRegisterPath = "/client/register"
	kiroAuthPath     = "/device_authorization"
)

// kiroStart registers this login's OIDC client, then asks it for a device code.
// Both addresses sit on the token endpoint's host, so the origin the data names
// is the one every call goes to.
func kiroStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	origin := originOf(f.TokenURL)
	body := `{"clientName":"` + kiroClientName + `","clientType":"` + kiroClientType +
		`","scopes":[` + quoteList(kiroScopes) + `],"grantTypes":["urn:ietf:params:oauth:grant-type:device_code","refresh_token"],"issuerUrl":"` + kiroIssueURL + `"}`
	status, raw, err := c.postJSON(ctx, origin+kiroRegisterPath, body, h)
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	clientID := gjson.GetBytes(raw, "clientId").String()
	clientSecret := gjson.GetBytes(raw, "clientSecret").String()
	if clientID == "" || clientSecret == "" {
		return DeviceGrant{}, &Error{ClassMalformed}
	}

	authBody, _ := sjson.SetBytes([]byte(`{"startUrl":"`+kiroStartURL+`"}`), "clientId", clientID)
	authBody, _ = sjson.SetBytes(authBody, "clientSecret", clientSecret)
	status, raw, err = c.postJSON(ctx, origin+kiroAuthPath, string(authBody), h)
	if err != nil {
		return DeviceGrant{}, err
	}
	if status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return DeviceGrant{}, &Error{StatusClass(status)}
	}
	code := gjson.GetBytes(raw, "deviceCode").String()
	user := gjson.GetBytes(raw, "userCode").String()
	page := gjson.GetBytes(raw, "verificationUriComplete").String()
	if page == "" {
		page = gjson.GetBytes(raw, "verificationUri").String()
	}
	if code == "" || user == "" || !plainHTTPS(page) {
		return DeviceGrant{}, &Error{ClassMalformed}
	}
	grant := DeviceGrant{DeviceCode: code, UserCode: user, VerificationURI: page}
	if n := gjson.GetBytes(raw, "expiresIn").Int(); n > 0 {
		grant.ExpiresIn = clampLife(n)
	}
	if n := gjson.GetBytes(raw, "interval").Int(); n > 0 {
		grant.Interval = clampInterval(n)
	}
	// The client pair is this login's own credential and never reaches the
	// browser: the poll and every renewal sign with it.
	grant.Extra, _ = sjson.Set("", "clientId", clientID)
	grant.Extra, _ = sjson.Set(grant.Extra, "clientSecret", clientSecret)
	return grant, nil
}

// kiroPoll trades the device code for the token. AWS answers with a camelCase
// body and names a waiting login with an error code, not a status.
func kiroPoll(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
	clientID, clientSecret := kiroClient(s.Extra)
	if clientID == "" || clientSecret == "" {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	body, _ := sjson.SetBytes([]byte(`{"grantType":"urn:ietf:params:oauth:grant-type:device_code"}`), "clientId", clientID)
	body, _ = sjson.SetBytes(body, "clientSecret", clientSecret)
	body, _ = sjson.SetBytes(body, "deviceCode", s.Code)
	status, raw, err := c.postJSON(ctx, f.TokenURL, string(body), h)
	if err != nil {
		return Tokens{}, pollDone, err
	}
	if !gjson.ValidBytes(raw) {
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	switch gjson.GetBytes(raw, "error").String() {
	case "authorization_pending", "slow_down":
		return Tokens{}, pollPending, nil
	case "expired_token":
		return Tokens{}, pollDone, &Error{ClassExpired}
	case "access_denied":
		return Tokens{}, pollDone, &Error{ClassDenied}
	}
	if status < 200 || status > 299 {
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	access := gjson.GetBytes(raw, "accessToken").String()
	if access == "" || len(access) > maxDeviceCode {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	t := Tokens{Access: access, Refresh: gjson.GetBytes(raw, "refreshToken").String()}
	if n := gjson.GetBytes(raw, "expiresIn").Int(); n > 0 {
		t.ExpiresIn = n
	}
	psd, _ := sjson.Set("", "clientId", clientID)
	psd, _ = sjson.Set(psd, "clientSecret", clientSecret)
	if arn := gjson.GetBytes(raw, "profileArn").String(); arn != "" {
		psd, _ = sjson.Set(psd, "profileArn", arn)
	}
	t.PSD = psd
	return t, pollDone, nil
}

// kiroRefresh renews a login's token with the client pair it registered.
func kiroRefresh(ctx context.Context, c *TokenClient, f Flow, refreshToken string, h map[string]string) (Tokens, error) {
	// The pair travels on the account's own data, which the refresher hands to
	// the flow; without it the call cannot be made at all.
	clientID, clientSecret := f.ClientID, f.ClientSecret
	if clientID == "" || clientSecret == "" || refreshToken == "" {
		return Tokens{}, &Error{ClassExpired}
	}
	body, _ := sjson.SetBytes([]byte(`{"grantType":"refresh_token"}`), "clientId", clientID)
	body, _ = sjson.SetBytes(body, "clientSecret", clientSecret)
	body, _ = sjson.SetBytes(body, "refreshToken", refreshToken)
	status, raw, err := c.postJSON(ctx, f.TokenURL, string(body), h)
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 || !gjson.ValidBytes(raw) {
		return Tokens{}, &Error{StatusClass(status)}
	}
	access := gjson.GetBytes(raw, "accessToken").String()
	if access == "" {
		return Tokens{}, &Error{ClassMalformed}
	}
	t := Tokens{Access: access, Refresh: gjson.GetBytes(raw, "refreshToken").String()}
	if t.Refresh == "" {
		t.Refresh = refreshToken
	}
	if n := gjson.GetBytes(raw, "expiresIn").Int(); n > 0 {
		t.ExpiresIn = n
	}
	return t, nil
}

// kiroClient reads the client pair out of the JSON a start or an account kept.
func kiroClient(raw string) (id, secret string) {
	if !gjson.Valid(raw) {
		return "", ""
	}
	return gjson.Get(raw, "clientId").String(), gjson.Get(raw, "clientSecret").String()
}

// originOf is the scheme and host of an address, which is where the other
// calls of the same service live.
func originOf(raw string) string {
	if i := strings.Index(raw, "://"); i > 0 {
		if j := strings.IndexByte(raw[i+3:], '/'); j >= 0 {
			return raw[:i+3+j]
		}
	}
	return strings.TrimRight(raw, "/")
}

// quoteList writes each word as a JSON string, comma separated.
func quoteList(words string) string {
	parts := strings.Fields(words)
	for i, p := range parts {
		parts[i] = document.Encode(p)
	}
	return strings.Join(parts, ",")
}
