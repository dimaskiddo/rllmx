package controllers

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
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

type asCaller struct{ c identity.Caller }

func (asCaller) Session(*nethttp.Request) bool { return false }
func (a asCaller) APIKey(*nethttp.Request) identity.KeyResult {
	return identity.KeyResult{Status: identity.KeyValid, Caller: a.c}
}
func (asCaller) RequireLogin(context.Context) bool  { return true }
func (asCaller) RequireAPIKey(context.Context) bool { return true }

type modelsRig struct {
	t    *testing.T
	st   *store.Store
	cat  func() (*catalog.Catalog, error)
	call func(string) *httptest.ResponseRecorder
}

func newModelsRig(t *testing.T, caller identity.Caller) *modelsRig {
	t.Helper()
	st := store.Migrated(t)
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	c := &catalog.Catalog{
		Registry: reg, Caps: caps, Conns: st.Connections(), Nodes: st.Nodes(), KV: st.KV(), Created: 1700000000,
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(routing.NewRepositorySource(st.Store), nil)},
	}
	r := &modelsRig{t: t, st: st, cat: func() (*catalog.Catalog, error) { return c, nil }}
	route := tests.NewRoute(t)
	g := apphttp.NewGuard(asCaller{caller}, true)
	route.SetGlobalMiddleware([]contractshttp.Middleware{&middleware.RequestID{}, &middleware.Guard{Guard: g}})
	m := &ModelsController{Catalog: func() (*catalog.Catalog, error) { return r.cat() }, Log: &tests.Log{}}
	g.Handle(route, apphttp.ClassLLM, nethttp.MethodGet, "/v1/models", m.List)
	g.Handle(route, apphttp.ClassLLM, nethttp.MethodGet, "/v1/models/*path", m.Show)
	g.Handle(route, apphttp.ClassLLM, nethttp.MethodGet, "/v1beta/models", m.Gemini)
	r.call = func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(nethttp.MethodGet, target, nil)
		req.RemoteAddr = "203.0.113.9:5000"
		rec := httptest.NewRecorder()
		route.ServeHTTP(rec, req)
		return rec
	}
	return r
}

func (r *modelsRig) openai(key string) {
	r.t.Helper()
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai", AuthType: "apikey", Name: "o", Fields: map[string]string{"apiKey": `"` + key + `"`},
	}, nil); err != nil {
		r.t.Fatal(err)
	}
}

func TestModelsListIsEmptyWithoutAConnectionAndShapedWithOne(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k"})
	rec := r.call("/v1/models")
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "object").String() != "list" || gjson.Get(rec.Body.String(), "data.#").Int() != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	r.openai("sk-test")
	rec = r.call("/v1/models")
	first := gjson.Get(rec.Body.String(), "data.0")
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") ||
		!strings.HasPrefix(first.Get("id").String(), "openai/") || first.Get("object").String() != "model" ||
		first.Get("created").Int() != 1700000000 || first.Get("owned_by").String() != "openai" ||
		first.Get("context_length").Int() <= 0 || first.Get("max_completion_tokens").Int() <= 0 || !first.Get("capabilities.tools").Exists() {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-test") {
		t.Fatal("a credential reached the list")
	}
}

func TestShowFindsAModelWithASlashInItsIdAndInfoAnswersFromTheSameList(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k"})
	r.openai("sk-test")
	id := gjson.Get(r.call("/v1/models").Body.String(), "data.0.id").String()

	rec := r.call("/v1/models/" + id)
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "id").String() != id {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	rec = r.call("/v1/models/info?id=" + id)
	if rec.Code != 200 || gjson.Get(rec.Body.String(), "kind").String() != "llm" || gjson.Get(rec.Body.String(), "endpoint").String() != "/v1/chat/completions" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := r.call("/v1/models/info"); rec.Code != 400 || gjson.Get(rec.Body.String(), "error.message").String() == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, p := range []string{"/v1/models/nope/none", "/v1/models/info?id=nope/none", "/v1/models/" + strings.ToUpper(id)} {
		rec := r.call(p)
		if rec.Code != 404 || gjson.Get(rec.Body.String(), "error.code").String() != "model_not_found" {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

func TestGeminiListUsesTheSameModelsWithRealLimits(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k"})
	r.openai("sk-test")
	id := gjson.Get(r.call("/v1/models").Body.String(), "data.0.id").String()
	ctxLen := gjson.Get(r.call("/v1/models").Body.String(), "data.0.context_length").Int()
	rec := r.call("/v1beta/models")
	first := gjson.Get(rec.Body.String(), "models.0")
	if rec.Code != 200 || first.Get("name").String() != "models/"+id || first.Get("inputTokenLimit").Int() != ctxLen ||
		!strings.Contains(first.Get("supportedGenerationMethods").Raw, "streamGenerateContent") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if gjson.Get(rec.Body.String(), "nextPageToken").Exists() {
		t.Fatal("a page token would send an SDK back for another page")
	}
}

func TestARestrictedKeyIsNotToldAboutOtherModels(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k", Restricted: true, Allow: []string{"openai/gpt-5.5"}})
	r.openai("sk-test")
	rec := r.call("/v1/models")
	if gjson.Get(rec.Body.String(), "data.#").Int() != 1 || gjson.Get(rec.Body.String(), "data.0.id").String() != "openai/gpt-5.5" {
		t.Fatalf("%s", rec.Body.String())
	}
	for _, p := range []string{"/v1/models/openai/gpt-5.4", "/v1/models/info?id=openai/gpt-5.4"} {
		if rec := r.call(p); rec.Code != 404 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := r.call("/v1/models/openai/gpt-5.5"); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestModelsErrorsAreOpenAIShapedAndCarryNoDetail(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k"})
	r.cat = func() (*catalog.Catalog, error) { return nil, errors.New("stores closed") }
	rec := r.call("/v1/models")
	if rec.Code != 503 || gjson.Get(rec.Body.String(), "error.type").String() == "" || strings.Contains(rec.Body.String(), "stores closed") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := r.call("/v1beta/models"); rec.Code != 503 || gjson.Get(rec.Body.String(), "error.status").String() != "UNAVAILABLE" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestAFailedReadAnswers500WithNoDetail(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k"})
	c, _ := r.cat()
	c.Conns = failingConns{}
	rec := r.call("/v1/models")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "db is gone") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

type failingConns struct{}

func (failingConns) List(context.Context, repository.ConnFilter) ([]repository.Connection, error) {
	return nil, errors.New("db is gone")
}

func TestListingModelsWritesNoConnectionRow(t *testing.T) {
	r := newModelsRig(t, identity.Caller{ID: "k"})
	r.openai("sk-test")
	before, _ := r.st.Connections().List(context.Background(), repository.ConnFilter{})
	r.call("/v1/models")
	after, _ := r.st.Connections().List(context.Background(), repository.ConnFilter{})
	if len(before) != 1 || len(after) != 1 || before[0].UpdatedAt != after[0].UpdatedAt || before[0].Data != after[0].Data {
		t.Fatalf("%+v %+v", before, after)
	}
}
