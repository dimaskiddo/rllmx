package providers

import (
	"context"
	"fmt"
	"time"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/catalog"
	"rllmx/app/http/controllers"
	"rllmx/app/routing"
)

// CatalogSource builds the model catalog on first use, for the same reason the
// pipeline is: the stores are not open when the routes are declared.
type CatalogSource struct{ lazy[catalog.Catalog] }

// NewCatalogSource returns a source that reads the bound pieces from app.
func NewCatalogSource(app foundation.Application) *CatalogSource {
	return &CatalogSource{lazy[catalog.Catalog]{build: func() (*catalog.Catalog, error) { return buildCatalog(app) }}}
}

// Get returns the catalog, building it if no earlier call did.
func (s *CatalogSource) Get() (*catalog.Catalog, error) { return s.get() }

func buildCatalog(app foundation.Application) (*catalog.Catalog, error) {
	h := HTTPOf(app)
	store := DataOf(app).Runner.Store()
	if store == nil {
		return nil, ErrStoreNotOpen
	}
	c := &catalog.Catalog{
		Registry: h.Registry, Caps: h.Capabilities,
		Resolver: &routing.Resolver{Registry: h.Registry, Snapshot: routing.NewSnapshot(routing.NewRepositorySource(store), nil)},
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
