package google

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"rllmx/app/llm/oauth"
	"strconv"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor/google"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
)

const (
	// googleTokenURL is where a service-account assertion and an ADC refresh go.
	googleTokenURL = "https://oauth2.googleapis.com/token"
	vertexScope    = "https://www.googleapis.com/auth/cloud-platform"
	jwtBearer      = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	assertionLife  = time.Hour
	// vertexLead renews a token this long before it ends.
	vertexLead   = 5 * time.Minute
	maxVertexTTL = 24 * time.Hour
)

// ErrBadKey means a Vertex key could not be used to get a token. It never carries
// part of the key.
var ErrBadKey = errors.New("oauth: the Vertex key is not a usable service account or credential file")

// GoogleMinter turns a Vertex key into a bearer token: a service account signs a
// one-hour assertion that Google trades for a token, and user credentials
// refresh with their own client. A raw API key needs no token. One request for a
// token is in flight per key however many callers ask, and a token is kept until
// five minutes before it ends. The key and the token are never logged, stored or
// returned.
type GoogleMinter struct {
	Tokens *oauth.TokenClient
	// Cache collapses concurrent mints; nil mints without collapsing.
	Cache *cache.Cache
	Log   log.Log
	Now   func() time.Time
	// URL replaces the token address; tests point it at a fake.
	URL string
}

func (m *GoogleMinter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *GoogleMinter) url() string {
	if m.URL != "" {
		return m.URL
	}
	return googleTokenURL
}

// VertexTokens adapts a GoogleMinter to the executor's token source: a transport
// failure becomes the executor's "unreachable", every other failure stays a failure
// of the key.
type VertexTokens struct{ Minter *GoogleMinter }

// Token implements the executor's TokenSource.
func (v VertexTokens) Token(ctx context.Context, key upstream.CloudKey) (string, error) {
	tok, err := v.Minter.Token(ctx, key)
	if oauth.ClassOf(err) == oauth.ClassTransport {
		return "", google.ErrTokenUnreachable
	}
	return tok, err
}

// Token returns a bearer token for the key. A raw key has none: it returns "".
func (m *GoogleMinter) Token(ctx context.Context, key upstream.CloudKey) (string, error) {
	if key.Kind == upstream.KeyRaw {
		return "", nil
	}
	id := dialect.HashKey(key.Kind, key.Account, key.ClientID, key.RefreshToken, key.PrivateKey)
	load := func(ctx context.Context) ([]byte, error) {
		token, ttl, err := m.mint(ctx, key)
		if err != nil {
			return nil, err
		}
		return []byte(strconv.FormatInt(m.now().Add(ttl).Unix(), 10) + "\n" + token), nil
	}
	var raw []byte
	var err error
	if m.Cache != nil {
		raw, err = m.Cache.Load(ctx, cache.Connections, "vertex:"+id, vertexLead, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return "", err
	}
	_, token, _ := strings.Cut(string(raw), "\n")
	return token, nil
}

func (m *GoogleMinter) mint(ctx context.Context, key upstream.CloudKey) (string, time.Duration, error) {
	fields := map[string]string{}
	switch key.Kind {
	case upstream.KeyServiceAccount:
		jwt, err := signAssertion(key, m.url(), m.now())
		if err != nil {
			return "", 0, ErrBadKey
		}
		fields["grant_type"], fields["assertion"] = jwtBearer, jwt
	case upstream.KeyADC:
		fields["grant_type"], fields["refresh_token"] = "refresh_token", key.RefreshToken
		fields["client_id"], fields["client_secret"] = key.ClientID, key.ClientSecret
	default:
		return "", 0, ErrBadKey
	}
	status, raw, err := m.Tokens.Post(ctx, m.url(), upstream.EncodingForm, fields, nil)
	if err != nil {
		return "", 0, err
	}
	if status < 200 || status > 299 {
		return "", 0, &oauth.Error{Class: oauth.StatusClass(status)}
	}
	token := gjson.GetBytes(raw, "access_token").String()
	if !oauth.BearerText(token) {
		return "", 0, &oauth.Error{Class: oauth.ClassMalformed}
	}
	ttl := time.Duration(gjson.GetBytes(raw, "expires_in").Int()) * time.Second
	if ttl <= 0 || ttl > maxVertexTTL {
		ttl = assertionLife
	}
	return token, ttl, nil
}

// signAssertion builds the RS256 JWT a service account trades for a token. The
// audience is the token address it will be posted to.
func signAssertion(key upstream.CloudKey, audience string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(key.PrivateKey, `\n`, "\n")))
	if block == nil {
		return "", ErrBadKey
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", ErrBadKey
	}
	priv, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return "", ErrBadKey
	}
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": key.Account, "scope": vertexScope, "aud": audience,
		"iat": now.Unix(), "exp": now.Add(assertionLife).Unix(),
	})
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		return "", ErrBadKey
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
