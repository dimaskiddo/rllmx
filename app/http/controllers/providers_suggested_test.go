package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/tests"
)

// serveGet answers one path with its query, which serve keeps out of the route.
func serveGet(t *testing.T, route, target string, handler contractshttp.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Get(route, handler)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func fetchOnce(body string) (*ProvidersController, *int) {
	calls := 0
	c := &ProvidersController{Fetch: func(context.Context, string) ([]byte, error) {
		calls++
		return []byte(body), nil
	}}
	return c, &calls
}

func TestSuggestedModelsFiltersOnlyTheProvidersOwnList(t *testing.T) {
	list := `{"data":[
		{"id":"big","name":"Big","context_length":300000,"pricing":{"prompt":"0","completion":"0"}},
		{"id":"small","context_length":8000,"pricing":{"prompt":"0","completion":"0"}},
		{"id":"paid","context_length":400000,"pricing":{"prompt":"1","completion":"2"}}]}`
	c, calls := fetchOnce(list)
	rec := serveGet(t, "/api/providers/suggested-models", "/api/providers/suggested-models?provider=openrouter", c.SuggestedModels)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"id":"big"`) || strings.Contains(body, `"small"`) || strings.Contains(body, `"paid"`) {
		t.Fatalf("%d %s", rec.Code, body)
	}
	// The second ask is answered from the kept list.
	serveGet(t, "/api/providers/suggested-models", "/api/providers/suggested-models?provider=openrouter", c.SuggestedModels)
	if *calls != 1 {
		t.Fatalf("the list was read %d times", *calls)
	}
	if rec := serveGet(t, "/api/providers/suggested-models", "/api/providers/suggested-models?provider=nobody", c.SuggestedModels); rec.Code != 400 {
		t.Fatalf("unknown provider: %d", rec.Code)
	}
}

func TestSuggestedModelsReadsTheFreeLaneFilter(t *testing.T) {
	list := `{"data":[{"id":"muse-spark-1.2-contributor-free"},{"id":"union-alpha"},{"id":"deepseek-v4-flash-free"}]}`
	c, _ := fetchOnce(list)
	rec := serveGet(t, "/api/providers/suggested-models", "/api/providers/suggested-models?provider=opencode", c.SuggestedModels)
	body := rec.Body.String()
	if !strings.Contains(body, "muse-spark-1.2-contributor-free") || strings.Contains(body, "union-alpha") || strings.Contains(body, "deepseek-v4-flash-free") {
		t.Fatalf("%s", body)
	}
}

func TestKiloFreeModelsKeepsItsListAndAnswersItWhenTheRefreshFails(t *testing.T) {
	c := &ProvidersController{Fetch: func(context.Context, string) ([]byte, error) {
		return []byte(`{"data":[{"id":"a","isFree":true},{"id":"b","isFree":false}]}`), nil
	}}
	rec := serve(t, "/api/providers/kilo/free-models", c.KiloFreeModels, http.MethodGet)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"a"`) || strings.Contains(rec.Body.String(), `"id":"b"`) || !strings.Contains(rec.Body.String(), `"cached":false`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	c.kiloAt = c.kiloAt.Add(-2 * kiloTTL)
	c.Fetch = func(context.Context, string) ([]byte, error) { return nil, errors.New("down") }
	rec = serve(t, "/api/providers/kilo/free-models", c.KiloFreeModels, http.MethodGet)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cached":true`) || !strings.Contains(rec.Body.String(), `"warning"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestASpelledDifferentlyAskIsTheSameKeptList(t *testing.T) {
	c, calls := fetchOnce(`{"data":[{"id":"big","context_length":300000,"pricing":{"prompt":"0","completion":"0"}}]}`)
	for _, q := range []string{"openrouter", "%20openrouter", "openrouter%20", "%20openrouter%20"} {
		serveGet(t, "/api/providers/suggested-models", "/api/providers/suggested-models?provider="+q, c.SuggestedModels)
	}
	if *calls != 1 || len(c.suggestAt) != 1 {
		t.Fatalf("%d reads and %d kept lists for one provider", *calls, len(c.suggestAt))
	}
}

func TestKiloFreeModelsAreReadOncePerHour(t *testing.T) {
	c, calls := fetchOnce(`{"data":[{"id":"a","isFree":true}]}`)
	for range 3 {
		if rec := serve(t, "/api/providers/kilo/free-models", c.KiloFreeModels, http.MethodGet); rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	if *calls != 1 {
		t.Fatalf("the gateway was read %d times inside the hour", *calls)
	}
}
