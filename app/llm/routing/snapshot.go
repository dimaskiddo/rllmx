package routing

import (
	"context"
	"time"

	"rllmx/app/platform/ttl"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

// SnapshotTTL is how long a read of stored data is reused. A second instance
// can lag by this much.
const SnapshotTTL = 5 * time.Second

// Node is the part of a provider node the resolver needs.
type Node struct {
	ID     string
	Type   string
	Prefix string
}

// Source is where the resolver reads stored data. A failed read is an error and
// never a guess.
type Source interface {
	Aliases(ctx context.Context) (map[string]string, error)
	Nodes(ctx context.Context) ([]Node, error)
	ComboNames(ctx context.Context) ([]string, error)
	// ActiveProviders lists the provider ids that have an active connection,
	// each once.
	ActiveProviders(ctx context.Context) ([]string, error)
}

// Snapshot reads aliases, nodes, combo names and active providers through a
// Source, each at most once per SnapshotTTL. A failed read is returned and not
// cached.
type Snapshot struct {
	aliases *ttl.Value[map[string]string]
	nodes   *ttl.Value[[]Node]
	combos  *ttl.Value[[]string]
	active  *ttl.Value[[]string]
}

// loadTimeout bounds one read of stored data, which no caller's context owns.
const loadTimeout = 10 * time.Second

// NewSnapshot wraps a source. now may be nil for the wall clock.
func NewSnapshot(src Source, now func() time.Time) *Snapshot {
	return &Snapshot{
		aliases: ttl.New(SnapshotTTL, now, src.Aliases),
		nodes:   ttl.New(SnapshotTTL, now, src.Nodes),
		combos:  ttl.New(SnapshotTTL, now, src.ComboNames),
		active:  ttl.New(SnapshotTTL, now, src.ActiveProviders),
	}
}

// Invalidate drops every cached read at once.
func (s *Snapshot) Invalidate() {
	s.aliases.Drop()
	s.nodes.Drop()
	s.combos.Drop()
	s.active.Drop()
}

// Aliases returns the user's model aliases as stored JSON text per name.
func (s *Snapshot) Aliases(ctx context.Context) (map[string]string, error) {
	return s.aliases.Get(ctx, loadTimeout)
}

// Nodes returns the provider nodes.
func (s *Snapshot) Nodes(ctx context.Context) ([]Node, error) { return s.nodes.Get(ctx, loadTimeout) }

// ComboNames returns the names of every combo.
func (s *Snapshot) ComboNames(ctx context.Context) ([]string, error) {
	return s.combos.Get(ctx, loadTimeout)
}

// ActiveProviders returns the provider ids that have an active connection.
func (s *Snapshot) ActiveProviders(ctx context.Context) ([]string, error) {
	return s.active.Get(ctx, loadTimeout)
}

type repositorySource struct{ store *repository.Store }

// NewRepositorySource reads aliases, nodes and combos from the configuration
// database.
func NewRepositorySource(store *repository.Store) Source { return repositorySource{store} }

func (r repositorySource) Aliases(ctx context.Context) (map[string]string, error) {
	return r.store.KV().List(ctx, repository.ScopeModelAliases)
}

func (r repositorySource) Nodes(ctx context.Context) ([]Node, error) {
	rows, err := r.store.Nodes().List(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]Node, 0, len(rows))
	for _, n := range rows {
		prefix, _ := document.From(n.Data).GetString("prefix")
		out = append(out, Node{ID: n.ID, Type: n.Type, Prefix: prefix})
	}
	return out, nil
}

func (r repositorySource) ComboNames(ctx context.Context) ([]string, error) {
	rows, err := r.store.Combos().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, c := range rows {
		out = append(out, c.Name)
	}
	return out, nil
}

func (r repositorySource) ActiveProviders(ctx context.Context) ([]string, error) {
	active := true
	rows, err := r.store.Connections().List(ctx, repository.ConnFilter{IsActive: &active})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rows))
	out := make([]string, 0, len(rows))
	for _, c := range rows {
		if c.Provider == "" || seen[c.Provider] {
			continue
		}
		seen[c.Provider] = true
		out = append(out, c.Provider)
	}
	return out, nil
}
