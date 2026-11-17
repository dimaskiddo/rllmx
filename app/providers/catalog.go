package providers

import (
	"context"
	"fmt"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/gateway/catalog"
	"rllmx/app/http/controllers"
	"rllmx/app/llm/routing"
)

// CatalogSource builds the model catalog on first use, for the same reason the
// pipeline is: the stores are not open when the routes are declared.
type CatalogSource struct{ lazy[catalog.Catalog] }

// NewCatalogSource returns a source that reads the bound pieces from app.
func NewCatalogSource(app foundation.Application) *CatalogSource {
	return &CatalogSource{lazy[catalog.Catalog]{build: func(context.Context) (*catalog.Catalog, error) { return buildCatalog(app) }}}
}

// Get returns the catalog, building it if no earlier call did.
func (s *CatalogSource) Get() (*catalog.Catalog, error) { return s.get() }

// SnapshotSource builds the one routing snapshot every reader shares. A catalog
// write invalidates it, so the next request routes the new alias, combo or node
// rather than waiting out the snapshot's TTL.
type SnapshotSource struct{ lazy[routing.Snapshot] }

// NewSnapshotSource returns a source that reads the store through app.
func NewSnapshotSource(app foundation.Application) *SnapshotSource {
	return &SnapshotSource{lazy[routing.Snapshot]{build: func(context.Context) (*routing.Snapshot, error) { return buildSnapshot(app) }}}
}

// Get returns the snapshot, building it if no earlier call did.
func (s *SnapshotSource) Get() (*routing.Snapshot, error) { return s.get() }

// Invalidate drops what the snapshot holds. One that was never built holds
// nothing.
func (s *SnapshotSource) Invalidate() {
	if v := s.v.Load(); v != nil {
		v.Invalidate()
	}
}

func buildSnapshot(app foundation.Application) (*routing.Snapshot, error) {
	store := DataOf(app).Runner.Store()
	if store == nil {
		return nil, ErrStoreNotOpen
	}
	return routing.NewSnapshot(routing.NewRepositorySource(store), nil), nil
}

func buildCatalog(app foundation.Application) (*catalog.Catalog, error) {
	h := HTTPOf(app)
	store := DataOf(app).Runner.Store()
	if store == nil {
		return nil, ErrStoreNotOpen
	}
	snap, err := h.Snapshot.Get()
	if err != nil {
		return nil, err
	}
	c := &catalog.Catalog{
		Registry: h.Registry, Caps: h.Capabilities,
		Resolver: &routing.Resolver{Registry: h.Registry, Snapshot: snap},
		Conns:    store.Connections(), Nodes: store.Nodes(), KV: store.KV(),
		Fetch: nodeFetch(h.Checker), Log: app.MakeLog(), Created: time.Now().Unix(),
	}
	if ch := cacheOrNil(app); ch != nil {
		c.Cache = ch
	}
	return c, nil
}

// nodeFetch asks a node for its model list through the checker's guarded client.
func nodeFetch(ck *controllers.Checker) catalog.NodeFetch {
	if ck == nil {
		return nil
	}
	return func(ctx context.Context, nodeType, baseURL, key string) ([]catalog.Model, error) {
		list, out := ck.ListNodeModels(ctx, controllers.Node{Type: nodeType, BaseURL: baseURL}, key)
		if out != controllers.OutcomeValid {
			return nil, fmt.Errorf("node model list: %s", out.Message())
		}
		models := make([]catalog.Model, 0, len(list))
		for _, m := range list {
			models = append(models, catalog.Model{ID: m.ID, Name: m.Name, Source: "node"})
		}
		return models, nil
	}
}
