package controllers

import (
	"net/http"
	"sort"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
)

// catalogProvider is the presentational half of a registry entry: what the
// dashboard shows and which login it can offer.
type catalogProvider struct {
	ID            string       `json:"id"`
	Alias         string       `json:"alias,omitempty"`
	Name          string       `json:"name"`
	Category      string       `json:"category"`
	Kinds         []string     `json:"kinds"`
	Regions       []string     `json:"regions,omitempty"`
	DefaultRegion string       `json:"defaultRegion,omitempty"`
	Login         *oauth.Offer `json:"login,omitempty"`
}

// Catalog answers GET /api/providers/catalog. The dashboard has no bundled
// provider table, so it reads the names, categories and login shapes here; a
// URL, a scope and a client id never leave the process.
func (c *ProvidersController) Catalog(ctx contractshttp.Context) contractshttp.Response {
	if c.Registry == nil {
		return flat(ctx, http.StatusServiceUnavailable, "Providers are not available")
	}
	c.catalogOnce.Do(func() { c.catalog = c.buildCatalog() })
	return ctx.Response().Json(http.StatusOK, contractshttp.Json{"providers": c.catalog})
}

// buildCatalog reads every registry entry once, sorted by name.
func (c *ProvidersController) buildCatalog() []catalogProvider {
	entries := c.Registry.All()
	out := make([]catalogProvider, 0, len(entries))
	for _, e := range entries {
		v := catalogProvider{
			ID: e.ID, Alias: e.Alias, Name: e.Display.Name,
			Category: e.Category, Kinds: catalogKinds(e),
			DefaultRegion: e.Transport.DefaultRegion,
		}
		if v.Name == "" {
			v.Name = e.ID
		}
		for _, r := range e.Transport.Regions {
			if r.ID != "" {
				v.Regions = append(v.Regions, r.ID)
			}
		}
		if c.OAuth != nil {
			if o, ok := c.OAuth.Offer(e); ok {
				v.Login = &o
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// catalogKinds is the chat kinds a provider offers the dashboard. A provider
// with no media block serves chat.
func catalogKinds(e upstream.Entry) []string {
	if e.Media == nil || len(e.Media.ServiceKinds) == 0 {
		return []string{upstream.KindLLM}
	}
	hidden := make(map[string]bool, len(e.Media.HiddenKinds))
	for _, k := range e.Media.HiddenKinds {
		hidden[k] = true
	}
	out := make([]string, 0, len(e.Media.ServiceKinds))
	for _, k := range e.Media.ServiceKinds {
		if !hidden[k] {
			out = append(out, k)
		}
	}
	return out
}
