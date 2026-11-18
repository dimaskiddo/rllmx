package routing

import (
	"context"
	"errors"
	"strings"
)

// Allow is one key's model list as it resolves: the combos it may call by name
// and the provider/model pairs it may call. It is the catalog's rule and the
// request's rule, so a listed model and a served model cannot disagree.
type Allow struct {
	combos map[string]bool
	models map[string]bool
}

// Resolves is what the matcher needs of a resolver: the catalog and the pipeline
// pass what they hold, which is this one call.
type Resolves interface {
	Resolve(ctx context.Context, input string) (Target, error)
}

// NewAllow resolves every entry the way a request resolves, so the two can only
// agree by naming the same target. An entry the resolver refuses allows nothing;
// a storage error is returned, so a caller fails closed rather than open.
func NewAllow(ctx context.Context, r Resolves, list []string) (Allow, error) {
	a := Allow{combos: map[string]bool{}, models: map[string]bool{}}
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
