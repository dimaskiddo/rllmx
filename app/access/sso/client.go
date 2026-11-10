package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	nethttp "net/http"

	"rllmx/app/platform/file"
	"rllmx/app/platform/network"
)

// The identity-provider client's own knobs: a short timeout, a few redirects, a
// small answer cap. A provider that needs more is misconfigured.
const (
	clientTimeout  = 10 * time.Second
	clientRedirect = 3
	clientBodyCap  = 1 << 20
)

// ErrUnreachable and ErrAnswer are the two failures a caller reports as
// `sso_idp_error`. Neither carries provider text, so neither can reach a log or
// a response by accident.
var (
	ErrUnreachable = errors.New("sso: the identity provider could not be reached")
	ErrAnswer      = errors.New("sso: the identity provider answered with an error")
)

// Client talks to one identity provider. It is the guarded client every
// outbound call outside the provider executors uses, with the SSO knobs.
type Client struct {
	http *nethttp.Client
}

// NewClient builds the client. allow is the operator's list; a private-network
// identity provider needs an entry, and the guard refuses everything else.
func NewClient(allow *network.Allow) *Client {
	return &Client{http: network.NewClient(network.Options{
		ConnectTimeout: clientTimeout,
		MaxRedirects:   clientRedirect,
		BodyCap:        clientBodyCap,
		Allow:          allow,
	})}
}

// GetJSON fetches a URL and decodes a JSON object into out.
func (c *Client) GetJSON(ctx context.Context, target string, out any) error {
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, target, nil)
	if err != nil {
		return ErrUnreachable
	}
	req.Header.Set("Accept", "application/json")
	return c.do(req, out)
}

// PostForm posts a form and decodes a JSON object into out. The token endpoint
// of every provider takes this shape.
func (c *Client) PostForm(ctx context.Context, target string, form url.Values, out any) error {
	req, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return ErrUnreachable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return c.do(req, out)
}

// do runs one request and decodes a bounded JSON answer.
func (c *Client) do(req *nethttp.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnreachable
	}
	defer resp.Body.Close()

	body, err := file.ReadCapped(resp.Body, clientBodyCap)
	if err != nil {
		return ErrAnswer
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%w: status %d", ErrAnswer, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return ErrAnswer
	}
	return nil
}
