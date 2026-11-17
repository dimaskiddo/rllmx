package controllers

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/tidwall/gjson"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/catalog"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// TestTagsListOnlyWhatTheCallerMayCall proves the Ollama list is the client
// catalog: a restricted key sees its allow list, and the answer is a real tag
// shape, not the reference's fixed fake list.
func TestTagsListOnlyWhatTheCallerMayCall(t *testing.T) {
	st := store.Migrated(t)
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai", AuthType: "apikey", Name: "a", Fields: map[string]string{"apiKey": `"sk-test"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	cat := &catalog.Catalog{
		Registry: reg, Caps: caps, Conns: st.Connections(), Nodes: st.Nodes(), KV: st.KV(), Created: 1700000000,
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(routing.NewRepositorySource(st.Store), nil)},
	}
	route := tests.NewRoute(t)
	caller := identity.Caller{ID: "k", Restricted: true, Allow: []string{"openai/gpt-4o"}}
	g := apphttp.NewGuard(asCaller{caller}, true)
	route.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	m := &ModelsController{Catalog: func() (*catalog.Catalog, error) { return cat, nil }, Log: &tests.Log{}}
	g.Handle(route, apphttp.ClassLLM, nethttp.MethodGet, "/api/tags", m.Tags)

	req := httptest.NewRequest(nethttp.MethodGet, "/api/tags", nil)
	req.RemoteAddr = "203.0.113.9:5000"
	rec := httptest.NewRecorder()
	route.ServeHTTP(rec, req)
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "models.#").Int() != 1 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	first := gjson.Get(rec.Body.String(), "models.0")
	if first.Get("name").String() != "openai/gpt-4o" || first.Get("model").String() != "openai/gpt-4o" ||
		first.Get("details.family").String() != "openai" || len(first.Get("digest").String()) != 64 ||
		first.Get("modified_at").String() == "" || first.Get("size").Int() != 0 {
		t.Fatalf("%s", first.Raw)
	}
}
