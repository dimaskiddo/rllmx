// Package zed talks to Zed's hosted LLM service. The account's own access token
// is exchanged for a short-lived one before a call, and the model catalog rides
// on that same token.
package zed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/platform/cache"
	"rllmx/app/platform/file"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

// The fixed addresses of the hosted service and its sign-in page.
const (
	CloudURL = "https://cloud.zed.dev"
	WebURL   = "https://zed.dev"

	// TokenTTL is how long a minted token is kept: the service issues one for an
	// hour, and it is replaced well before that.
	TokenTTL = 50 * time.Minute
	// CatalogTTL is how long the model catalog is kept.
	CatalogTTL = time.Hour
	// NativeAppPort is the port the native sign-in answers on.
	NativeAppPort = 58443
)

var (
	// ErrNoToken means the account holds nothing to exchange.
	ErrNoToken = errors.New("zed: the account has no access token")
	// ErrRefused means the service refused the exchange or the catalog.
	ErrRefused = errors.New("zed: the service refused the request")
	// ErrUnreachable means the service could not be reached at all.
	ErrUnreachable = errors.New("zed: the service could not be reached")
	// ErrUnknownTarget means the catalog names no model, or no format for it.
	ErrUnknownTarget = errors.New("zed: the catalog does not serve this model")
)

// Account is what one login stored.
type Account struct {
	Access, UserID, SystemID, OrganizationID string
}

// Client exchanges a token and reads the catalog. A mint in flight serves every
// caller waiting on it, and the result is kept in the memory-only cache.
type Client struct {
	HTTP  *http.Client
	Cache *cache.Cache
	// Base replaces the cloud address, for a test server.
	Base string
}

// base is the cloud address in force.
func (c *Client) base() string {
	if c.Base != "" {
		return strings.TrimSuffix(c.Base, "/")
	}
	return CloudURL
}

// Token returns a hosted token for the account, minting one when the cached one
// is gone. force skips the cache, for a token the service refused.
func (c *Client) Token(ctx context.Context, a Account, force bool) (string, error) {
	if a.Access == "" {
		return "", ErrNoToken
	}
	org := strings.TrimSpace(a.OrganizationID)
	if org == "" {
		found, err := c.organization(ctx, a)
		if err != nil {
			return "", err
		}
		org = found
	}
	if org == "" {
		return "", ErrRefused
	}
	key := "zed:" + dialect.HashKey(a.UserID, org, a.Access)
	if force && c.Cache != nil {
		c.Cache.Delete(cache.Connections, key)
	}
	load := func(ctx context.Context) ([]byte, error) {
		tok, err := c.mint(ctx, a, org)
		if err != nil {
			return nil, err
		}
		return []byte(tok), nil
	}
	var raw []byte
	var err error
	if c.Cache != nil {
		raw, err = c.Cache.Load(ctx, cache.Connections, key, TokenTTL, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// mint trades the account's token for a hosted one.
func (c *Client) mint(ctx context.Context, a Account, org string) (string, error) {
	status, raw, err := c.call(ctx, http.MethodPost, c.base()+"/client/llm_tokens", a,
		`{"organization_id":`+document.Encode(org)+`}`, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", ErrRefused
	}
	tok := gjson.GetBytes(raw, "token")
	switch {
	case tok.Type == gjson.String && tok.String() != "":
		return tok.String(), nil
	case tok.IsArray() && tok.Get("0").String() != "":
		return tok.Get("0").String(), nil
	case tok.Get("value").String() != "":
		return tok.Get("value").String(), nil
	}
	return "", ErrRefused
}

// organization reads the account's own organization, for a login that stored
// none: its default, else its personal one, else the first.
func (c *Client) organization(ctx context.Context, a Account) (string, error) {
	status, raw, err := c.call(ctx, http.MethodGet, c.base()+"/client/users/me", a, "", nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", ErrRefused
	}
	for _, p := range []string{"default_organization_id", "defaultOrganizationId"} {
		if v := gjson.GetBytes(raw, p).String(); v != "" {
			return v, nil
		}
	}
	orgs := gjson.GetBytes(raw, "organizations")
	for _, o := range orgs.Array() {
		if o.Get("is_personal").Bool() {
			return o.Get("id").String(), nil
		}
	}
	if first := orgs.Get("0.id").String(); first != "" {
		return first, nil
	}
	return "", nil
}

// call sends one request on the account's own token.
func (c *Client) call(ctx context.Context, method, target string, a Account, body string, extra map[string]string) (int, []byte, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return 0, nil, ErrUnreachable
	}
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", UserAuth(a))
	if a.SystemID != "" {
		req.Header.Set("x-zed-system-id", a.SystemID)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, ErrUnreachable
	}
	defer resp.Body.Close()
	raw, err := file.ReadCapped(resp.Body, 1<<20)
	if err != nil && !errors.Is(err, file.ErrTooLarge) {
		return 0, nil, ErrUnreachable
	}
	return resp.StatusCode, raw, nil
}

// UserAuth is the account's own token as the service reads it: the user id and
// the token, separated by a space.
func UserAuth(a Account) string {
	if a.UserID == "" {
		return a.Access
	}
	return a.UserID + " " + a.Access
}
