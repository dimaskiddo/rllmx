// Package catalog answers one question before a request is sent: does the
// account's provider know this model? A provider that says no is refused here,
// with the client's own 400, so the account is never sent a request it cannot
// serve and no lock is written for a model that does not exist.
package catalog

import (
	"context"
	"errors"
	"slices"
	"time"

	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
	"rllmx/app/store/repository"
)

// Verdict is what a provider's own list says about a model.
type Verdict int

const (
	// Undetermined means the list could not be read: a fetch failure, a refusal
	// or a timeout. The request is sent, because a provider's outage is not the
	// client's mistake.
	Undetermined Verdict = iota
	// Known means the provider lists the model.
	Known
	// Unknown means the provider answered and the model was not in it.
	Unknown
)

// TTL is how long one account's list is kept. A model added upstream reaches a
// client within this window.
const TTL = 5 * time.Minute

// ErrNoCatalog means the provider has no fetcher here, so nothing is known.
var ErrNoCatalog = errors.New("catalog: the provider has no model list")

// Fetcher reads the model ids a provider serves for one account. A fetcher that
// cannot read the list returns an error, never an empty list.
type Fetcher func(ctx context.Context, conn repository.Connection) ([]string, error)

// Guard asks a provider's own model list. It holds the fetchers, the cache and
// the accounts to ask; a nil Guard is valid and always answers Undetermined.
type Guard struct {
	Cache    *cache.Cache
	Fetchers map[string]Fetcher
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// Live reads the provider's own list for the account, through the same five
// minute cache the request guard uses. It answers false when the provider has
// no fetcher or the read failed.
func (g *Guard) Live(ctx context.Context, provider string, conn repository.Connection) ([]string, bool) {
	if g == nil {
		return nil, false
	}
	fetch, ok := g.Fetchers[provider]
	if !ok {
		return nil, false
	}
	ids, err := g.list(ctx, provider, conn, fetch)
	if err != nil || len(ids) == 0 {
		return nil, false
	}
	return ids, true
}

// Known reports what the provider's list says about model for this account.
// The answer is Undetermined when the provider has no fetcher, when the model
// is empty, or when the list could not be read.
func (g *Guard) Known(ctx context.Context, provider string, conn repository.Connection, model string) Verdict {
	if g == nil || model == "" {
		return Undetermined
	}
	fetch, ok := g.Fetchers[provider]
	if !ok {
		return Undetermined
	}
	ids, err := g.list(ctx, provider, conn, fetch)
	if err != nil || len(ids) == 0 {
		return Undetermined
	}
	if slices.Contains(ids, model) {
		return Known
	}
	return Unknown
}

// list reads one account's list, through the cache when there is one.
func (g *Guard) list(ctx context.Context, provider string, conn repository.Connection, fetch Fetcher) ([]string, error) {
	key := dialect.HashKey("catalog", provider, conn.ID)
	load := func(ctx context.Context) ([]byte, error) {
		ids, err := fetch(ctx, conn)
		if err != nil {
			return nil, err
		}
		return encode(ids), nil
	}
	if g.Cache == nil {
		ids, err := fetch(ctx, conn)
		return ids, err
	}
	raw, err := g.Cache.Load(ctx, cache.Connections, key, TTL, load)
	if err != nil {
		return nil, err
	}
	return decode(raw), nil
}

// encode and decode keep a list in the cache as one id per line: an id is a
// plain token, so a separator that cannot appear in one is enough.
func encode(ids []string) []byte {
	var out []byte
	for _, id := range ids {
		out = append(out, id...)
		out = append(out, '\n')
	}
	return out
}

func decode(raw []byte) []string {
	var out []string
	for len(raw) > 0 {
		i := 0
		for i < len(raw) && raw[i] != '\n' {
			i++
		}
		if i > 0 {
			out = append(out, string(raw[:i]))
		}
		raw = raw[i:]
		if len(raw) > 0 {
			raw = raw[1:]
		}
	}
	return out
}
