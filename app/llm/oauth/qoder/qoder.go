package qoder

import (
	"context"
	"net/http"
	"net/url"
	"rllmx/app/access/oidc"
	"rllmx/app/llm/oauth"
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
func Start(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, h map[string]string) (oauth.DeviceGrant, error) {
	verifier, err := oidc.Verifier()
	if err != nil {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassTransport}
	}
	machine, err := oauth.NewNonce()
	if err != nil {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassTransport}
	}
	nonce, err := oauth.NewNonce()
	if err != nil {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassTransport}
	}
	login := f.Params
	base := ""
	for _, p := range login {
		if p.Name == "loginUrl" {
			base = p.Value
		}
	}
	if base == "" {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassRejected}
	}
	u, err := qoderURL(base, map[string]string{
		"challenge": oidc.Challenge(verifier), "challenge_method": "S256",
		"machine_id": machine, "nonce": nonce,
	})
	if err != nil {
		return oauth.DeviceGrant{}, &oauth.Error{Class: oauth.ClassRejected}
	}
	return oauth.DeviceGrant{
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
func Poll(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, s oauth.DeviceState, h map[string]string) (oauth.Tokens, oauth.PollState, error) {
	nonce, ok := strings.CutPrefix(s.Code, "nonce\x00")
	if !ok || nonce == "" {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassMalformed}
	}
	u, err := qoderURL(f.TokenURL, map[string]string{
		"nonce": nonce, "verifier": s.Verifier, "challenge_method": "S256",
	})
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.ClassRejected}
	}
	status, raw, err := c.Get(ctx, u, oauth.WithEntry(oauth.WithEntry(h, "User-Agent", qoderAgent), "Accept", "application/json"))
	if err != nil {
		return oauth.Tokens{}, oauth.PollDone, err
	}
	switch {
	case status == http.StatusAccepted, status == http.StatusNotFound:
		return oauth.Tokens{}, oauth.PollPending, nil
	case status < 200 || status > 299 || !gjson.ValidBytes(raw):
		return oauth.Tokens{}, oauth.PollDone, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	token := document.FirstString(gjson.GetBytes(raw, "token"), gjson.GetBytes(raw, "access_token"))
	if token == "" {
		return oauth.Tokens{}, oauth.PollPending, nil
	}
	return oauth.Tokens{
		Access: token,
		Email:  oauth.CleanEmail(document.FirstString(gjson.GetBytes(raw, "email"))),
		UserID: document.FirstString(gjson.GetBytes(raw, "user_id"), gjson.GetBytes(raw, "userId")),
		PSD:    `{"machineId":` + document.Encode(s.Extra) + `}`,
	}, oauth.PollDone, nil
}

// qoderNoRefresh reports that a gateway login is never renewed: the token is
// long lived and the account signs in again when it ends.
func NoRefresh(context.Context, *oauth.TokenClient, oauth.Flow, string, map[string]string) (oauth.Tokens, error) {
	return oauth.Tokens{}, &oauth.Error{Class: oauth.ClassInvalidGrant}
}

// qoderAccount reads the account's identity and organization. Both steps are
// best effort: a login whose calls fail still connects.
func Account(ctx context.Context, c *oauth.TokenClient, f oauth.Flow, t oauth.Tokens, h map[string]string) (oauth.Account, error) {
	userID := t.UserID
	acc := oauth.Account{NoRefresh: true}
	u := f.Endpoints[upstream.EndpointUser]
	if u != "" && t.Access != "" {
		status, raw, err := c.Get(ctx, u+"/api/v1/userinfo", oauth.WithEntry(h, "Authorization", "Bearer "+t.Access))
		if err == nil && status >= 200 && status <= 299 && gjson.ValidBytes(raw) {
			if id := document.FirstString(gjson.GetBytes(raw, "id"), gjson.GetBytes(raw, "userId"), gjson.GetBytes(raw, "user_id")); id != "" {
				userID = id
			}
			acc.Email = oauth.CleanEmail(document.FirstString(gjson.GetBytes(raw, "email")))
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
		return "", oauth.ErrSession
	}
	q := u.Query()
	for k, v := range params {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
