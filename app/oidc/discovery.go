package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"

	"rllmx/app/cache"
	"rllmx/app/network"
	"rllmx/app/sso"
)

// DiscoveryTTL is how long one issuer's metadata is cached: an hour, per PRD 2.
const DiscoveryTTL = time.Hour

// ErrDiscovery means the issuer's metadata could not be read or is unusable.
// It names no URL, because the URL came from a caller-chosen setting.
var ErrDiscovery = errors.New("oidc: the issuer metadata could not be read")

// Document is an issuer's OpenID configuration, reduced to what this program
// uses. A field it does not need is not decoded, so a provider's extra fields
// cost nothing.
type Document struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// Discover returns the issuer's metadata, from the cache while it is fresh. A
// failure is returned and never cached, which cache.Load guarantees.
func Discover(ctx context.Context, c *sso.Client, store *cache.Cache, issuer string) (Document, error) {
	canonical, err := ParseIssuer(issuer)
	if err != nil {
		return Document{}, ErrDiscovery
	}
	key := cacheKey(canonical)

	load := func(ctx context.Context) ([]byte, error) {
		var doc Document
		if err := c.GetJSON(ctx, canonical+"/.well-known/openid-configuration", &doc); err != nil {
			return nil, ErrDiscovery
		}
		return validateDocument(canonical, doc)
	}
	if store == nil {
		raw, err := load(ctx)
		if err != nil {
			return Document{}, err
		}
		return decodeDocument(raw)
	}
	raw, err := store.Load(ctx, cache.Discovery, key, DiscoveryTTL, load)
	if err != nil {
		return Document{}, ErrDiscovery
	}
	return decodeDocument(raw)
}

// Purge drops every cached discovery document, after a settings write.
func Purge(store *cache.Cache) {
	if store != nil {
		store.Purge(cache.Discovery)
	}
}

// validateDocument refuses metadata whose own issuer differs from the one
// discovered: a document that names another issuer is a mix-up attack.
func validateDocument(issuer string, doc Document) ([]byte, error) {
	if doc.Issuer != issuer {
		return nil, ErrDiscovery
	}
	for _, raw := range []string{doc.AuthorizationEndpoint, doc.TokenEndpoint, doc.JWKSURI} {
		if _, err := ParseEndpoint(raw); err != nil {
			return nil, ErrDiscovery
		}
	}
	return json.Marshal(doc)
}

func decodeDocument(raw []byte) (Document, error) {
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Document{}, ErrDiscovery
	}
	return doc, nil
}

// ParseEndpoint checks an endpoint from a discovery document: https, or http on
// this machine, with a host. A query and a fragment are allowed here, because
// some providers put a tenant id in them.
func ParseEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: an endpoint is missing", ErrDiscovery)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", ErrDiscovery
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && network.IsLoopbackHost(u.Hostname())) {
		return "", ErrDiscovery
	}
	return raw, nil
}

// cacheKey is the hash of the issuer, so a long URL stays a short key.
func cacheKey(issuer string) string {
	sum := sha256.Sum256([]byte(issuer))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// SortedScopes is the space-joined scope list the authorization request sends.
func SortedScopes(raw string) string {
	s := Scopes(raw)
	sort.Strings(s)
	return strings.Join(s, " ")
}

// Keys fetches the provider's published signing keys, cached with the metadata's
// lifetime. A failure is never cached.
func Keys(ctx context.Context, c *sso.Client, store *cache.Cache, jwksURI string) (*jose.JSONWebKeySet, error) {
	canonical, err := ParseEndpoint(jwksURI)
	if err != nil {
		return nil, ErrDiscovery
	}
	load := func(ctx context.Context) ([]byte, error) {
		var raw json.RawMessage
		if err := c.GetJSON(ctx, canonical, &raw); err != nil {
			return nil, ErrDiscovery
		}
		set := new(jose.JSONWebKeySet)
		if err := json.Unmarshal(raw, set); err != nil || len(set.Keys) == 0 {
			return nil, ErrDiscovery
		}
		return []byte(raw), nil
	}
	if store == nil {
		raw, err := load(ctx)
		if err != nil {
			return nil, err
		}
		return decodeKeys(raw)
	}
	raw, err := store.Load(ctx, cache.Discovery, "keys:"+cacheKey(canonical), DiscoveryTTL, load)
	if err != nil {
		return nil, ErrDiscovery
	}
	return decodeKeys(raw)
}

func decodeKeys(raw []byte) (*jose.JSONWebKeySet, error) {
	set := new(jose.JSONWebKeySet)
	if err := json.Unmarshal(raw, set); err != nil {
		return nil, ErrDiscovery
	}
	return set, nil
}
