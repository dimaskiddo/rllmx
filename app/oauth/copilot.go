package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/cache"
	"rllmx/app/repository"
	"rllmx/app/text"
	"rllmx/app/upstream"
)

const (
	// mintLead renews a Copilot token this long before it runs out.
	mintLead = 5 * time.Minute
	// mintResult keeps one mint for the callers that ask while it is in flight
	// and just after.
	mintResult  = 10 * time.Second
	maxMinted   = 4096
	mintedField = "copilotToken"
	mintedUntil = "copilotTokenExpiresAt"
)

// mintHeaders are the data-named headers the mint call carries; the others a
// login sends belong to chat requests.
var mintHeaders = map[string]bool{"user-agent": true, "editor-version": true, "editor-plugin-version": true, "x-github-api-version": true}

// mint trades a GitHub token for a short-lived Copilot token. The token and its
// expiry (Unix seconds) are all that is read from the answer.
func mint(ctx context.Context, c *TokenClient, o *upstream.OAuth, githubToken string) (token string, expires int64, err error) {
	headers := map[string]string{"Authorization": "token " + githubToken}
	for k, v := range o.Headers {
		if mintHeaders[strings.ToLower(k)] {
			headers[k] = v
		}
	}
	status, raw, err := c.get(ctx, o.Endpoints[upstream.EndpointCopilotToken], headers)
	if err != nil {
		return "", 0, err
	}
	if status < 200 || status > 299 {
		if status >= 500 {
			return "", 0, &Error{ClassTransport}
		}
		return "", 0, &Error{ClassRejected}
	}
	token = gjson.GetBytes(raw, "token").String()
	expires = gjson.GetBytes(raw, "expires_at").Int()
	if token == "" || len(token) > maxMinted || text.HasASCIIControl(token) || expires <= 0 {
		return "", 0, &Error{ClassMalformed}
	}
	return token, expires, nil
}

// PSDStore is the part of the connection store the minter writes to.
type PSDStore interface {
	PatchPSD(ctx context.Context, id string, set map[string]string, remove []string) error
}

var _ PSDStore = (*repository.ConnectionsRepo)(nil)

// Minter keeps the Copilot token of each GitHub account. One mint is in flight
// per account however many requests ask, and the token is stored sealed with
// the account so a restart does not mint for every account at once. The token
// is never logged, returned or put in an error.
type Minter struct {
	Registry *upstream.Registry
	Tokens   *TokenClient
	// Cache collapses concurrent mints; nil mints without collapsing.
	Cache *cache.Cache
	// Store returns the connection store, or nil before it is open.
	Store func() PSDStore
	Log   log.Log
	Now   func() time.Time
}

func (m *Minter) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Token returns a Copilot token for the account: the stored one while it has
// more than five minutes left, else a new one.
func (m *Minter) Token(ctx context.Context, conn repository.Connection) (string, error) {
	psd, _ := conn.Doc().GetRaw("providerSpecificData")
	token, until := gjson.Get(psd, mintedField).String(), gjson.Get(psd, mintedUntil).Int()
	if token != "" && time.Unix(until, 0).Sub(m.now()) > mintLead {
		return token, nil
	}
	return m.fresh(ctx, conn, "")
}

// Renew replaces a token the provider refused. If the stored token is no longer
// the refused one, someone renewed it already and it is returned as it is.
func (m *Minter) Renew(ctx context.Context, conn repository.Connection, rejected string) (string, error) {
	psd, _ := conn.Doc().GetRaw("providerSpecificData")
	if cur := gjson.Get(psd, mintedField).String(); cur != "" && cur != rejected {
		return cur, nil
	}
	return m.fresh(ctx, conn, rejected)
}

func (m *Minter) fresh(ctx context.Context, conn repository.Connection, rejected string) (string, error) {
	e, ok := m.Registry.ByID(conn.Provider)
	if !ok || e.OAuth == nil || e.OAuth.Mint != upstream.MintCopilot {
		return "", &Error{ClassRejected}
	}
	github, _ := conn.Doc().GetString("accessToken")
	if github == "" {
		return "", ErrNoToken
	}
	sum := sha256.Sum256([]byte(conn.ID + "\x00" + github + "\x00" + rejected))
	key := hex.EncodeToString(sum[:])
	load := func(ctx context.Context) ([]byte, error) {
		token, expires, err := mint(ctx, m.Tokens, e.OAuth, github)
		if err != nil {
			return nil, err
		}
		m.keep(ctx, conn.ID, token, expires)
		return []byte(strconv.FormatInt(expires, 10) + "\n" + token), nil
	}
	var raw []byte
	var err error
	if m.Cache != nil {
		raw, err = m.Cache.Load(ctx, cache.Connections, key, mintResult, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return "", err
	}
	_, token, _ := strings.Cut(string(raw), "\n")
	return token, nil
}

// keep stores the new token with the account. A failed write is only logged:
// the token still serves this process.
func (m *Minter) keep(ctx context.Context, id, token string, expires int64) {
	if m.Store == nil {
		return
	}
	st := m.Store()
	if st == nil {
		return
	}
	err := st.PatchPSD(ctx, id, map[string]string{mintedField: quote(token), mintedUntil: strconv.FormatInt(expires, 10)}, nil)
	if err != nil && m.Log != nil {
		m.Log.Warningf("oauth: storing a minted token failed")
	}
}
