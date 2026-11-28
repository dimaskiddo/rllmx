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
	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests/store"
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
	// One active account, so a refusal has an account it would have reached.
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: id, AuthType: "apikey", Name: "a", Fields: map[string]string{"apiKey": `"k"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	rows := &usage.Sink{}
	return gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{},
		Budget: b, Usage: rows, Now: time.Now,
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
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
	if got[0].ConnectionID == "" {
		t.Fatal("the refusal names no account")
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
	if got := rows.All(); len(got) != 1 || got[0].ErrorClass != "key_quota" || got[0].ConnectionID == "" {
		t.Fatalf("rows %+v", got)
	}
}

func TestABudgetThatCannotBeReadFailsClosed(t *testing.T) {
	p, rows := budgetPipeline(t, "openai", budgetImageEntry, spend{err: errors.New("analytics is down")})
	out := p.Images(context.Background(), gatewaymedia.Input{
		Path: "/v1/images/generations", Body: []byte(`{"model":"gpt-image-1","prompt":"p"}`),
		Caller: identity.Caller{ID: "k1"},
	})
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := rows.All(); len(got) != 1 || got[0].ErrorClass != "budget_unavailable" {
		t.Fatalf("rows %+v", got)
	}
}

// stalled blocks until the request ends, the way a store read holds the caller
// while the connection behind it closes.
type stalled struct{}

func (stalled) Check(ctx context.Context, _ string) (quota.Refusal, error) {
	<-ctx.Done()
	return quota.Refusal{}, ctx.Err()
}

func TestABudgetCheckTheClientAbandonedIsNotAServerFault(t *testing.T) {
	p, rows := budgetPipeline(t, "openai", budgetImageEntry, stalled{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	defer cancel()
	out := p.Images(ctx, gatewaymedia.Input{
		Path: "/v1/images/generations", Body: []byte(`{"model":"gpt-image-1","prompt":"p"}`),
		Caller: identity.Caller{ID: "k1"},
	})
	if out.Status != 499 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := rows.All(); len(got) != 1 || got[0].ErrorClass != "client_closed" {
		t.Fatalf("rows %+v", got)
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
