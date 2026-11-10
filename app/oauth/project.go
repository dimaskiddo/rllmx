package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/cache"
	"rllmx/app/repository"
	"rllmx/app/upstream"
)

const (
	// projectResult keeps one lookup for the callers that ask while it is in
	// flight and for a while after, so a burst makes one set of calls.
	projectResult = time.Hour
	// lookupTries and lookupPause bound the onboarding a lookup falls back to; a
	// request is waiting, so it is shorter than the login's.
	lookupTries = 2
	lookupPause = 12 * time.Second
)

// ProjectStore is the part of the connection store the resolver writes to.
type ProjectStore interface {
	SetProjectID(ctx context.Context, id, project string) (bool, error)
}

var _ ProjectStore = (*repository.ConnectionsRepo)(nil)

// ProjectResolver finds the Cloud Code project of a Google account that has none
// stored, and stores it. One lookup is in flight per account however many
// requests ask. It never invents a project: an account it cannot resolve has
// none, and the request fails with a fixed class.
type ProjectResolver struct {
	Registry *upstream.Registry
	Tokens   *TokenClient
	// Cache collapses concurrent lookups; nil looks up without collapsing.
	Cache *cache.Cache
	// Store returns the connection store, or nil before it is open.
	Store func() ProjectStore
	Log   log.Log
}

// Project returns the account's project: the stored one, else the one found by
// asking the provider with the account's token.
func (p *ProjectResolver) Project(ctx context.Context, conn repository.Connection, access string) (string, error) {
	if cur, _ := conn.Doc().GetString("projectId"); cur != "" {
		return cur, nil
	}
	e, ok := p.Registry.ByID(conn.Provider)
	if !ok || e.OAuth == nil || e.OAuth.Endpoints[upstream.EndpointLoadCodeAssist] == "" {
		return "", &Error{ClassRejected}
	}
	if access == "" {
		return "", ErrNoToken
	}
	f := Flow{Provider: e.ID, OAuth: *e.OAuth}
	sum := sha256.Sum256([]byte(conn.ID + "\x00" + access))
	key := hex.EncodeToString(sum[:])
	load := func(ctx context.Context) ([]byte, error) {
		project, tier := loadCodeAssist(ctx, p.Tokens, f, access, callHeaders(f, ""), e.ID == "gemini-cli")
		if project == "" {
			project = p.onboard(ctx, f, access, tier)
		}
		if project == "" {
			return nil, &Error{ClassRejected}
		}
		p.keep(ctx, conn.ID, project)
		return []byte(project), nil
	}
	var raw []byte
	var err error
	if p.Cache != nil {
		raw, err = p.Cache.Load(ctx, cache.Connections, key, projectResult, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// onboard enables the account the way a login does, with fewer tries.
func (p *ProjectResolver) onboard(ctx context.Context, f Flow, access, tier string) string {
	u := f.Endpoints[upstream.EndpointOnboardUser]
	if u == "" {
		return ""
	}
	body := `{"tierId":` + quote(tier) + `,"metadata":` + googleMetadata + `}`
	headers := withEntry(callHeaders(f, ""), "Authorization", "Bearer "+access)
	for try := range lookupTries {
		if try > 0 && p.Tokens.wait(ctx, lookupPause) != nil {
			return ""
		}
		if project, done := onboardOnce(ctx, p.Tokens, u, body, headers); done {
			return project
		}
	}
	return ""
}

// keep stores the project with the account. A failed write is only logged: the
// project still serves this process.
func (p *ProjectResolver) keep(ctx context.Context, id, project string) {
	if p.Store == nil {
		return
	}
	st := p.Store()
	if st == nil {
		return
	}
	if _, err := st.SetProjectID(ctx, id, project); err != nil && p.Log != nil {
		p.Log.Warningf("oauth: storing a project failed")
	}
}
