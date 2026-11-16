package routing

import (
	"context"
	"errors"
	"strings"

	"rllmx/app/llm/upstream"
)

// Allow is one key's model list as it resolves: the combos it may call by name,
// the provider/model pairs it may call, and the providers it may use where the
// provider is the model (a search, a fetch, a video poll). It is the catalog's
// rule and the request's rule, so a listed model and a served model cannot
// disagree.
type Allow struct {
	combos    map[string]bool
	models    map[string]bool
	providers map[string]bool
}

// Resolves is what the matcher needs of a resolver: the catalog and the pipeline
// pass what they hold, which is this one call.
type Resolves interface {
	Resolve(ctx context.Context, input string) (Target, error)
}

// NewAllow resolves every entry the way a request resolves, so the two can only
// agree by naming the same target. reg resolves a bare entry to a provider id,
// for a route whose identity is the provider alone; nil skips that set. An entry
// the resolver refuses allows nothing; a storage error is returned, so a caller
// fails closed rather than open.
func NewAllow(ctx context.Context, r Resolves, reg *upstream.Registry, list []string) (Allow, error) {
	a := Allow{combos: map[string]bool{}, models: map[string]bool{}, providers: map[string]bool{}}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		t, err := r.Resolve(ctx, entry)
		switch {
		case errors.Is(err, ErrEmpty), errors.Is(err, ErrTooLong), errors.Is(err, ErrBadInput):
			continue
		case err != nil:
			return Allow{}, err
		}
		if t.Combo != "" {
			a.combos[t.Combo] = true
			continue
		}
		a.models[t.Provider+"/"+t.Model] = true
		// An entry with no slash may also name a provider, for a route whose
		// identity is the provider alone.
		if !strings.Contains(entry, "/") && reg != nil {
			if id, ok := reg.Resolve(entry); ok {
				a.providers[id] = true
			}
		}
	}
	return a, nil
}

// Target reports whether a resolved target is on the list: a combo by name,
// otherwise the resolved provider and model. The comparison is exact, because
// the resolver's answer is the identity; a spelling that would route elsewhere
// never matches.
func (a Allow) Target(t Target) bool {
	if t.Combo != "" {
		return a.combos[t.Combo]
	}
	return a.models[t.Provider+"/"+t.Model]
}

// Provider reports whether a route whose identity is the provider alone is on
// the list.
func (a Allow) Provider(id string) bool { return a.providers[id] }
