package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
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
	// Email is what a poll that carries the account's address hands over; the
	// RFC 8628 answer has none.
	Email string
	// PSD is provider data a poll hands over, as a JSON object of short text.
	PSD string
}

// TokenClient calls token endpoints. It follows no redirect, because a moved
// endpoint would carry the code or refresh token elsewhere, and it reads at
// most one mebibyte.
type TokenClient struct {
	HTTP *http.Client
	// Sleep pauses between the tries of a hook; nil waits on a real timer.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now reads the clock for an expiry given as a date; nil is the real one.
	Now func() time.Time
}

func (c *TokenClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// wait pauses for d, or until ctx ends.
func (c *TokenClient) wait(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewTokenClient builds the client on the guarded dialer. allow is the
// operator's list of private addresses.
func NewTokenClient(allow *network.Allow) *TokenClient {
	return &TokenClient{HTTP: network.NewClient(network.Options{ConnectTimeout: connectTimeout, MaxRedirects: -1, Allow: allow})}
}

// Exchange trades an authorization code for tokens. A code dialect does it its
// own way; the rest send the standard call, with the client's secret when the
// application has one.
func (c *TokenClient) Exchange(ctx context.Context, f Flow, code, redirect, verifier, state string) (Tokens, error) {
	if d, ok := codeDialects[f.Dialect]; ok {
		return d.exchange(ctx, c, f, code, redirect)
	}
	fields := map[string]string{
		"grant_type": "authorization_code", "client_id": f.ClientID, "code": code,
		"redirect_uri": redirect,
	}
	if verifier != "" {
		fields["code_verifier"] = verifier
	}
	if f.EchoState {
		fields["state"] = state
	}
	if f.ClientSecret != "" {
		fields["client_secret"] = f.ClientSecret
	}
	return c.call(ctx, f.TokenURL, f.Exchange, fields, nil)
}

func (c *TokenClient) call(ctx context.Context, tokenURL, encoding string, fields, headers map[string]string) (Tokens, error) {
	status, raw, err := c.post(ctx, tokenURL, encoding, fields, headers)
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
func (c *TokenClient) post(ctx context.Context, target, encoding string, fields, headers map[string]string) (int, []byte, error) {
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
	return c.do(ctx, http.MethodPost, target, contentType, body, headers)
}

// get makes one bounded GET with the given headers and returns the status and
// the answer. A failure is already a class.
func (c *TokenClient) get(ctx context.Context, target string, headers map[string]string) (int, []byte, error) {
	return c.do(ctx, http.MethodGet, target, "", nil, headers)
}

// do sends one request. A header the caller names is set after the defaults, so
// it can replace Accept but never the content type of a body it did not build.
func (c *TokenClient) do(ctx context.Context, method, target, contentType string, body []byte, headers map[string]string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, tokenTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, &Error{ClassTransport}
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
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

// postJSON sends a JSON body that holds more than strings, for a hook's call.
func (c *TokenClient) postJSON(ctx context.Context, target, body string, headers map[string]string) (int, []byte, error) {
	return c.do(ctx, http.MethodPost, target, "application/json", []byte(body), headers)
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
// headers are the login's call headers for this account; when the data names a
// token header, the refresh token goes there too.
func (c *TokenClient) Refresh(ctx context.Context, f Flow, refreshToken string, headers map[string]string) (Tokens, error) {
	if f.Refresh.TokenHeader != "" {
		headers = withEntry(headers, f.Refresh.TokenHeader, refreshToken)
	}
	var t Tokens
	var err error
	switch d, dev := deviceDialects[f.Dialect]; {
	case codeDialects[f.Dialect].refresh != nil:
		t, err = codeDialects[f.Dialect].refresh(ctx, c, f, refreshToken, headers)
	case dev && d.refresh != nil:
		t, err = d.refresh(ctx, c, f, refreshToken, headers)
	default:
		fields := map[string]string{"grant_type": "refresh_token", "refresh_token": refreshToken, "client_id": f.ClientID}
		if f.ClientSecret != "" {
			fields["client_secret"] = f.ClientSecret
		}
		maps.Copy(fields, f.Refresh.Fields)
		t, err = c.call(ctx, c.refreshURL(f), f.Refresh.Encoding, fields, headers)
	}
	if err != nil {
		return Tokens{}, err
	}
	if t.Refresh == "" {
		t.Refresh = refreshToken
	}
	return t, nil
}

// refreshURL is where a renewal goes: its own address when the data names one,
// else the token address.
func (c *TokenClient) refreshURL(f Flow) string {
	if f.Refresh.URL != "" {
		return f.Refresh.URL
	}
	return f.TokenURL
}
