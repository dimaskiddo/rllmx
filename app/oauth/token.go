package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/file"
	"rllmx/app/network"
	"rllmx/app/upstream"
)

// Class says why a token call failed. It is all a caller ever learns: the
// provider's own text is never kept, logged or returned.
type Class string

const (
	ClassInvalidGrant Class = "invalid_grant"
	ClassTransport    Class = "transport"
	ClassRejected     Class = "rejected"
	ClassMalformed    Class = "malformed"
	// ClassExpired and ClassDenied end a device login: the code ran out, or the
	// user refused it.
	ClassExpired Class = "expired_token"
	ClassDenied  Class = "access_denied"
)

// Error is a failed token call.
type Error struct{ Class Class }

func (e *Error) Error() string { return "oauth: the token call failed: " + string(e.Class) }

// ClassOf returns the class of a token error, or "" for any other error.
func ClassOf(err error) Class {
	var e *Error
	if errors.As(err, &e) {
		return e.Class
	}
	return ""
}

const (
	tokenTimeout   = 15 * time.Second
	connectTimeout = 10 * time.Second
	maxTokenBody   = 1 << 20
)

// Tokens is what a token endpoint hands back.
type Tokens struct {
	Access    string
	Refresh   string
	ID        string
	Scope     string
	ExpiresIn int64
}

// TokenClient calls token endpoints. It follows no redirect, because a moved
// endpoint would carry the code or refresh token elsewhere, and it reads at
// most one mebibyte.
type TokenClient struct{ HTTP *http.Client }

// NewTokenClient builds the client on the guarded dialer. allow is the
// operator's list of private addresses.
func NewTokenClient(allow *network.Allow) *TokenClient {
	return &TokenClient{HTTP: network.NewClient(network.Options{ConnectTimeout: connectTimeout, MaxRedirects: -1, Allow: allow})}
}

// Exchange trades an authorization code for tokens.
func (c *TokenClient) Exchange(ctx context.Context, f Flow, code, redirect, verifier, state string) (Tokens, error) {
	fields := map[string]string{
		"grant_type": "authorization_code", "client_id": f.ClientID, "code": code,
		"redirect_uri": redirect, "code_verifier": verifier,
	}
	if f.EchoState {
		fields["state"] = state
	}
	return c.call(ctx, f.TokenURL, f.Exchange, fields)
}

func (c *TokenClient) call(ctx context.Context, tokenURL, encoding string, fields map[string]string) (Tokens, error) {
	status, raw, err := c.post(ctx, tokenURL, encoding, fields)
	if err != nil {
		return Tokens{}, err
	}
	if status < 200 || status > 299 {
		return Tokens{}, &Error{classify(raw)}
	}
	return parseTokens(raw)
}

// post sends one form or JSON body and returns the status and the bounded
// answer. Every failure is already a class.
func (c *TokenClient) post(ctx context.Context, target, encoding string, fields map[string]string) (int, []byte, error) {
	var body []byte
	var contentType string
	if encoding == upstream.EncodingJSON {
		body, _ = json.Marshal(fields)
		contentType = "application/json"
	} else {
		form := url.Values{}
		for k, v := range fields {
			form.Set(k, v)
		}
		body, contentType = []byte(form.Encode()), "application/x-www-form-urlencoded"
	}
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	defer resp.Body.Close()
	raw, err := file.ReadCapped(resp.Body, maxTokenBody)
	if errors.Is(err, file.ErrTooLarge) {
		return 0, nil, &Error{ClassMalformed}
	}
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	return resp.StatusCode, raw, nil
}

// get makes one bounded GET with the given headers and returns the status and
// the answer. A failure is already a class.
func (c *TokenClient) get(ctx context.Context, target string, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	defer resp.Body.Close()
	raw, err := file.ReadCapped(resp.Body, maxTokenBody)
	if errors.Is(err, file.ErrTooLarge) {
		return 0, nil, &Error{ClassMalformed}
	}
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	return resp.StatusCode, raw, nil
}

// classify reduces an error body to a class by the codes both providers use.
func classify(body []byte) Class {
	lower := strings.ToLower(string(body))
	for _, code := range []string{"invalid_grant", "refresh_token_reused", "refresh_token_expired", "refresh_token_invalidated"} {
		if strings.Contains(lower, code) {
			return ClassInvalidGrant
		}
	}
	return ClassRejected
}

func parseTokens(raw []byte) (Tokens, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return Tokens{}, &Error{ClassMalformed}
	}
	t := Tokens{
		Access:    gjson.GetBytes(raw, "access_token").String(),
		Refresh:   gjson.GetBytes(raw, "refresh_token").String(),
		ID:        gjson.GetBytes(raw, "id_token").String(),
		Scope:     gjson.GetBytes(raw, "scope").String(),
		ExpiresIn: max(gjson.GetBytes(raw, "expires_in").Int(), 0),
	}
	if t.Access == "" {
		return Tokens{}, &Error{ClassMalformed}
	}
	return t, nil
}

// Refresh trades a refresh token for new tokens. A provider that does not
// rotate its refresh token leaves the field out, and then the old one stays.
func (c *TokenClient) Refresh(ctx context.Context, f Flow, refreshToken string) (Tokens, error) {
	t, err := c.call(ctx, f.TokenURL, f.Refresh.Encoding, map[string]string{
		"grant_type": "refresh_token", "refresh_token": refreshToken, "client_id": f.ClientID,
	})
	if err != nil {
		return Tokens{}, err
	}
	if t.Refresh == "" {
		t.Refresh = refreshToken
	}
	return t, nil
}
