package zed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/cache"
)

// Model is one entry of the hosted catalog.
type Model struct {
	ID       string
	Name     string
	Provider string
	Disabled bool
}

// Catalog is the list of models an account may call, with the provider the
// service names for each. An unknown provider fails the request upstream, so a
// model the catalog does not name is refused here rather than sent.
type Catalog struct {
	Models  map[string]Model
	Default string
}

// Models reads the account's catalog, from the cache while it is fresh. A read
// the service refuses is reported; a caller decides whether that is fatal.
func (c *Client) Models(ctx context.Context, a Account) (Catalog, error) {
	tok, err := c.Token(ctx, a, false)
	if err != nil {
		return Catalog{}, err
	}
	sum := sha256.Sum256([]byte(a.UserID + "\x00" + a.OrganizationID + "\x00" + a.Access))
	key := "zed-catalog:" + hex.EncodeToString(sum[:])
	load := func(ctx context.Context) ([]byte, error) {
		return c.fetchCatalog(ctx, tok)
	}
	var raw []byte
	if c.Cache != nil {
		raw, err = c.Cache.Load(ctx, cache.Connections, key, CatalogTTL, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return Catalog{}, err
	}
	return parseCatalog(raw), nil
}

// fetchCatalog reads and shrinks the catalog to what a request needs.
func (c *Client) fetchCatalog(ctx context.Context, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+"/models", nil)
	if err != nil {
		return nil, ErrUnreachable
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-zed-client-supports-x-ai", "true")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, ErrUnreachable
	}
	defer resp.Body.Close()
	raw, err := readCapped(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, ErrRefused
	}
	return raw, nil
}

// modelID reads an id the service sends as a string or as an object holding it.
func modelID(v gjson.Result) string {
	switch {
	case v.Type == gjson.String:
		return v.String()
	case v.IsArray():
		return v.Get("0").String()
	case v.IsObject():
		return v.Get("id").String()
	}
	return ""
}

// parseCatalog reads the entries a request needs, skipping a disabled model.
func parseCatalog(raw []byte) Catalog {
	out := Catalog{Models: map[string]Model{}}
	g := gjson.ParseBytes(raw)
	for _, m := range g.Get("models").Array() {
		id := modelID(m.Get("id"))
		if id == "" || m.Get("is_disabled").Bool() {
			continue
		}
		provider := m.Get("provider").String()
		if provider == "" {
			continue
		}
		out.Models[id] = Model{ID: id, Name: m.Get("display_name").String(), Provider: provider}
	}
	out.Default = g.Get("default_model").String()
	return out
}

// WireFormats maps a catalog provider to the format the request is built in.
// The hosted service names them its own way; an unknown provider fails here,
// because the service answers an unrecognized one with a 500 before it looks at
// the model.
var WireFormats = map[string]string{
	"anthropic": "claude",
	"open_ai":   "openai-responses",
	"openai":    "openai-responses",
	"google":    "gemini",
	"gemini":    "gemini",
	"x_ai":      "openai",
	"xai":       "openai",
	"x-ai":      "openai",
}

// Format returns the wire format one model is reached in, or ErrUnknownTarget.
func (c *Client) Format(ctx context.Context, a Account, model string) (string, error) {
	cat, err := c.Models(ctx, a)
	if err != nil {
		return "", err
	}
	entry, ok := cat.Models[model]
	if !ok {
		return "", ErrUnknownTarget
	}
	format, ok := WireFormats[strings.ToLower(entry.Provider)]
	if !ok {
		return "", ErrUnknownTarget
	}
	return format, nil
}
