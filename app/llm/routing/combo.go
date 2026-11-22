package routing

import (
	"context"
	"errors"
	"strings"

	"rllmx/app/store/repository"
)

// MaxComboDepth bounds how deep a combo may name another, so a cycle cannot
// exhaust the stack and a misconfiguration is a clear refusal.
const MaxComboDepth = 3

var (
	// ErrComboCycle means a combo, directly or through another, names itself.
	ErrComboCycle = errors.New("routing: a combo names itself")
	// ErrComboDepth means combos are nested deeper than MaxComboDepth.
	ErrComboDepth = errors.New("routing: combos are nested too deeply")
	// ErrComboEmpty means a combo has no member a request can reach.
	ErrComboEmpty = errors.New("routing: the combo names no model")
)

// ComboList returns every stored combo in the order the dashboard lists them,
// which is the order auto plan picks a planning combo from.
func (r *Resolver) ComboList(ctx context.Context) ([]repository.Combo, error) {
	names, err := r.Snapshot.ComboNames(ctx)
	if err != nil {
		return nil, err
	}
	byName, err := r.Snapshot.Combos(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]repository.Combo, 0, len(names))
	for _, name := range names {
		if c, ok := byName[name]; ok {
			out = append(out, c)
		}
	}
	return out, nil
}

// Member is one target a combo may reach: what a member string resolves to.
type Member struct {
	Provider string
	Model    string
}

// Expand resolves a combo into its members in declared order, flattening a
// nested combo in place. A member that does not resolve is skipped: it is one
// entry of a list, not the whole request. A cycle, a depth past MaxComboDepth
// and a combo with nothing reachable are errors.
func (r *Resolver) Expand(ctx context.Context, name string) ([]Member, error) {
	combos, err := r.Snapshot.Combos(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Member
	if err := r.expand(ctx, combos, name, 1, seen, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrComboEmpty
	}
	return out, nil
}

// expand appends one combo's members, following a nested combo in place.
func (r *Resolver) expand(ctx context.Context, combos map[string]repository.Combo, name string, depth int, seen map[string]bool, out *[]Member) error {
	if seen[name] {
		return ErrComboCycle
	}
	if depth > MaxComboDepth+1 {
		return ErrComboDepth
	}
	seen[name] = true
	defer delete(seen, name)

	for _, entry := range combos[name].Models {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		t, err := r.Resolve(ctx, entry)
		switch {
		case errors.Is(err, ErrEmpty), errors.Is(err, ErrTooLong), errors.Is(err, ErrBadInput):
			continue
		case err != nil:
			return err
		}
		if t.Combo != "" {
			if err := r.expand(ctx, combos, t.Combo, depth+1, seen, out); err != nil {
				return err
			}
			continue
		}
		if t.Provider == "" || t.Model == "" {
			continue
		}
		*out = append(*out, Member{Provider: t.Provider, Model: t.Model})
	}
	return nil
}
