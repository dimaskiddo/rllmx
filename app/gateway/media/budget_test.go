package media_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	gatewaymedia "rllmx/app/gateway/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/llm/upstream"
	"rllmx/tests/usage"
)

// spend answers one budget check the same way every time.
type spend struct {
	ref quota.Refusal
	err error
}

func (s spend) Check(context.Context, string) (quota.Refusal, error) { return s.ref, s.err }

const (
	budgetImageEntry = `{"id":"openai","category":"apikey","display":{"name":"O"},
"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"https://api.example/v1","authType":"apikey","models":[{"id":"gpt-image-1"}]}}}`
	budgetSearchEntry = `{"id":"serper","category":"apikey","display":{"name":"S"},
"media":{"serviceKinds":["webSearch"],"searchConfig":{"baseUrl":"https://serper.example","authType":"apikey","authHeader":"x-api-key","searchTypes":["web"],"defaultMaxResults":5,"maxMaxResults":20}}}`
)

// budgetPipeline builds a pipeline whose only job is a budget check: no account
// and no client is needed when the check refuses first.
func budgetPipeline(t *testing.T, id, entry string, b gatewaymedia.Budget) (*gatewaymedia.Pipeline, *usage.Sink) {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{"data/" + id + ".json": {Data: []byte(entry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	rows := &usage.Sink{}
	return gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{},
		Budget: b, Usage: rows, Now: time.Now,
	}), rows
}

func spent() spend {
	return spend{ref: quota.Refusal{Refused: true, RetryAfter: time.Minute, Remaining: quota.Remaining{Requests: 0, Tokens: 0, Cost: 0}}}
}

func TestASpentBudgetStopsAnImageBeforeAnyCall(t *testing.T) {
	p, rows := budgetPipeline(t, "openai", budgetImageEntry, spent())
	out := p.Images(context.Background(), gatewaymedia.Input{
		Path: "/v1/images/generations", Body: []byte(`{"model":"gpt-image-1","prompt":"p"}`),
		Caller: identity.Caller{ID: "k1"},
	})
	if out.Status != http.StatusTooManyRequests || !strings.Contains(string(out.Body), "insufficient_quota") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if out.Header["Retry-After"] != "60" {
		t.Fatalf("%v", out.Header)
	}
	// The row names the model it asked for and the key's budget, not the
	// provider's pace.
	got := rows.All()
	if len(got) != 1 || got[0].ErrorClass != "key_quota" || got[0].Model != "gpt-image-1" {
		t.Fatalf("rows %+v", got)
	}
}

func TestASpentBudgetStopsASearchBeforeAnyCall(t *testing.T) {
	p, rows := budgetPipeline(t, "serper", budgetSearchEntry, spent())
	out := p.Search(context.Background(), gatewaymedia.Input{
		Path: "/v1/search", Body: []byte(`{"provider":"serper","query":"x"}`),
		Caller: identity.Caller{ID: "k1"},
	})
	if out.Status != http.StatusTooManyRequests || !strings.Contains(string(out.Body), "insufficient_quota") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := rows.All(); len(got) != 1 || got[0].ErrorClass != "key_quota" {
		t.Fatalf("rows %+v", got)
	}
}

func TestABudgetThatCannotBeReadFailsClosed(t *testing.T) {
	p, _ := budgetPipeline(t, "openai", budgetImageEntry, spend{err: errors.New("analytics is down")})
	out := p.Images(context.Background(), gatewaymedia.Input{
		Path: "/v1/images/generations", Body: []byte(`{"model":"gpt-image-1","prompt":"p"}`),
		Caller: identity.Caller{ID: "k1"},
	})
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestASearchRowCarriesTheQueryCost(t *testing.T) {
	f := &fakeServer{body: `{"organic":[{"title":"A","link":"https://example.com/a"}]}`}
	entry := `{"id":"serper","category":"apikey","display":{"name":"S"},
"media":{"serviceKinds":["webSearch"],"searchConfig":{"baseUrl":"@","authType":"apikey","authHeader":"x-api-key","searchTypes":["web"],"defaultMaxResults":5,"maxMaxResults":20,"costPerQuery":0.002}}}`
	r := newServiceRig(t, f, nil, map[string]string{"serper": entry}, serperKey())
	if out := r.p.Search(context.Background(), gatewaymedia.Input{
		Path: "/v1/search", Body: []byte(`{"provider":"serper","query":"x"}`), Caller: identity.Caller{ID: "k1"},
	}); out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	rows := r.sink.All()
	if len(rows) != 1 || rows[0].Cost != 0.002 {
		t.Fatalf("%+v", rows)
	}
}
