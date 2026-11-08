package routing

import (
	"context"
	"sync"
	"time"

	"rllmx/app/document"
	"rllmx/app/repository"
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
}

// Snapshot reads aliases, nodes and combo names through a Source, each at most
// once per SnapshotTTL. A failed read is returned and not cached.
type Snapshot struct {
	aliases cache[map[string]string]
	nodes   cache[[]Node]
	combos  cache[[]string]
}

// NewSnapshot wraps a source. now may be nil for the wall clock.
func NewSnapshot(src Source, now func() time.Time) *Snapshot {
	if now == nil {
		now = time.Now
	}
	return &Snapshot{
		aliases: cache[map[string]string]{load: src.Aliases, now: now},
		nodes:   cache[[]Node]{load: src.Nodes, now: now},
		combos:  cache[[]string]{load: src.ComboNames, now: now},
	}
}

// Invalidate drops every cached read at once.
func (s *Snapshot) Invalidate() {
	s.aliases.drop()
	s.nodes.drop()
	s.combos.drop()
}

// Aliases returns the user's model aliases as stored JSON text per name.
func (s *Snapshot) Aliases(ctx context.Context) (map[string]string, error) { return s.aliases.get(ctx) }

// Nodes returns the provider nodes.
func (s *Snapshot) Nodes(ctx context.Context) ([]Node, error) { return s.nodes.get(ctx) }

// ComboNames returns the names of every combo.
func (s *Snapshot) ComboNames(ctx context.Context) ([]string, error) { return s.combos.get(ctx) }

type cache[T any] struct {
	mu   sync.Mutex
	at   time.Time
	ok   bool
	val  T
	load func(context.Context) (T, error)
	now  func() time.Time
}

func (c *cache[T]) get(ctx context.Context) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok && c.now().Sub(c.at) < SnapshotTTL {
		return c.val, nil
	}
	v, err := c.load(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	c.val, c.at, c.ok = v, c.now(), true
	return v, nil
}

func (c *cache[T]) drop() {
	c.mu.Lock()
	c.ok = false
	c.mu.Unlock()
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
