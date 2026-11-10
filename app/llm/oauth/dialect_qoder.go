package oauth

import (
	"context"
	"net/http"
	"net/url"
	"rllmx/app/access/oidc"
	"rllmx/app/store/document"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/upstream"
)

// The gateway's own device login: a challenge the browser is sent with, a poll
// that answers 202 or 404 while it waits, and a user id the signing needs.
// qoderStart builds the sign-in address. The challenge and the machine id are
// made here and carried in the address; the poll proves the challenge.
func qoderStart(ctx context.Context, c *TokenClient, f Flow, h map[string]string) (DeviceGrant, error) {
	verifier, err := oidc.Verifier()
	if err != nil {
		return DeviceGrant{}, &Error{ClassTransport}
	}
	machine, err := NewNonce()
	if err != nil {
		return DeviceGrant{}, &Error{ClassTransport}
	}
	nonce, err := NewNonce()
	if err != nil {
		return DeviceGrant{}, &Error{ClassTransport}
	}
	login := f.Params
	base := ""
	for _, p := range login {
		if p.Name == "loginUrl" {
			base = p.Value
		}
	}
	if base == "" {
		return DeviceGrant{}, &Error{ClassRejected}
	}
	u, err := qoderURL(base, map[string]string{
		"challenge": oidc.Challenge(verifier), "challenge_method": "S256",
		"machine_id": machine, "nonce": nonce,
	})
	if err != nil {
		return DeviceGrant{}, &Error{ClassRejected}
	}
	return DeviceGrant{
		DeviceCode:      "nonce\x00" + nonce,
		VerificationURI: u,
		// The user reads nothing off the page: the browser finishes the sign-in
		// and the poll collects it.
		UserCode:  "",
		Interval:  qoderInterval,
		ExpiresIn: qoderLoginLife,
		Verifier:  verifier,
		Extra:     machine,
	}, nil
}

// qoderPoll asks whether the browser finished. 202 and 404 mean it has not.
func qoderPoll(ctx context.Context, c *TokenClient, f Flow, s DeviceState, h map[string]string) (Tokens, PollState, error) {
	nonce, ok := strings.CutPrefix(s.Code, "nonce\x00")
	if !ok || nonce == "" {
		return Tokens{}, pollDone, &Error{ClassMalformed}
	}
	u, err := qoderURL(f.TokenURL, map[string]string{
		"nonce": nonce, "verifier": s.Verifier, "challenge_method": "S256",
	})
	if err != nil {
		return Tokens{}, pollDone, &Error{ClassRejected}
	}
	status, raw, err := c.get(ctx, u, WithEntry(WithEntry(h, "User-Agent", qoderAgent), "Accept", "application/json"))
	if err != nil {
		return Tokens{}, pollDone, err
	}
	switch {
	case status == http.StatusAccepted, status == http.StatusNotFound:
		return Tokens{}, pollPending, nil
	case status < 200 || status > 299 || !gjson.ValidBytes(raw):
		return Tokens{}, pollDone, &Error{StatusClass(status)}
	}
	token := document.FirstString(gjson.GetBytes(raw, "token"), gjson.GetBytes(raw, "access_token"))
	if token == "" {
		return Tokens{}, pollPending, nil
	}
	return Tokens{
		Access: token,
		Email:  CleanEmail(document.FirstString(gjson.GetBytes(raw, "email"))),
		UserID: document.FirstString(gjson.GetBytes(raw, "user_id"), gjson.GetBytes(raw, "userId")),
		PSD:    `{"machineId":` + document.Encode(s.Extra) + `}`,
	}, pollDone, nil
}

// qoderNoRefresh reports that a gateway login is never renewed: the token is
// long lived and the account signs in again when it ends.
func qoderNoRefresh(context.Context, *TokenClient, Flow, string, map[string]string) (Tokens, error) {
	return Tokens{}, &Error{ClassInvalidGrant}
}

// qoderAccount reads the account's identity and organization. Both steps are
// best effort: a login whose calls fail still connects.
func qoderAccount(ctx context.Context, c *TokenClient, f Flow, t Tokens, h map[string]string) (Account, error) {
	userID := t.UserID
	acc := Account{NoRefresh: true}
	u := f.Endpoints[upstream.EndpointUser]
	if u != "" && t.Access != "" {
		status, raw, err := c.get(ctx, u+"/api/v1/userinfo", WithEntry(h, "Authorization", "Bearer "+t.Access))
		if err == nil && status >= 200 && status <= 299 && gjson.ValidBytes(raw) {
			if id := document.FirstString(gjson.GetBytes(raw, "id"), gjson.GetBytes(raw, "userId"), gjson.GetBytes(raw, "user_id")); id != "" {
				userID = id
			}
			acc.Email = CleanEmail(document.FirstString(gjson.GetBytes(raw, "email")))
		}
	}
	machine := gjson.Get(t.PSD, "machineId").String()
	psd := `{"authMethod":"device","userId":` + document.Encode(userID) + `,"machineId":` + document.Encode(machine) + `}`
	if org := document.FirstString(gjson.GetBytes([]byte(t.PSD), "organizationId")); org != "" {
		psd = strings.TrimSuffix(psd, "}") + `,"organizationId":` + document.Encode(org) + `}`
	}
	acc.PSD = psd
	return acc, nil
}

// qoderAgent is the client the gateway's own tool says it is.
const qoderAgent = "qodercli/1.0.0"

// qoderInterval and qoderLoginLife are how often the browser is asked, and how
// long the login waits for it.
const (
	qoderInterval  = 2 * time.Second
	qoderLoginLife = 300 * time.Second
)

// qoderURL adds the given parameters to an address.
func qoderURL(base string, params map[string]string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", ErrSession
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
