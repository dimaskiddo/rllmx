package media_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

// scriptAdapter is a media adapter whose Build returns a scripted error or a
// call the fake client answers.
type scriptAdapter struct {
	buildErr error
	kind     string
}

func (s scriptAdapter) Kind() string { return s.kind }
func (s scriptAdapter) Build(context.Context, media.Request, media.Client) (media.Call, error) {
	if s.buildErr != nil {
		return media.Call{}, s.buildErr
	}
	return media.Call{Method: http.MethodPost, URL: "https://x.example/i", Body: []byte(`{}`)}, nil
}
func (s scriptAdapter) Parse(context.Context, media.Request, media.Client, int, http.Header, []byte) (media.Response, error) {
	return media.Response{Status: 200, Header: map[string]string{"Content-Type": "application/json"}, Body: []byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgo="}]}`)}, nil
}

type table map[string]media.Adapter

func (t table) For(provider, kind, _ string) (media.Adapter, bool) {
	a, ok := t[provider]
	return a, ok
}

const edgeEntry = `{"id":"edge","category":"apikey","display":{"name":"E"},
"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"https://x.example/i","authType":"apikey","authHeader":"bearer","models":[{"id":"edge-img"}]}}}`

func edgeRegistry(t *testing.T) *upstream.Registry {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{"data/edge.json": {Data: []byte(edgeEntry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func twoAccounts(t *testing.T) *store.Store {
	t.Helper()
	st := store.Migrated(t)
	for _, name := range []string{"one", "two"} {
		if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
			Provider: "edge", AuthType: "apikey", Name: name, Fields: map[string]string{"apiKey": `"KEY"`},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func TestAPlanRefusalPassesOverTheAccountWithoutLockingIt(t *testing.T) {
	st := twoAccounts(t)
	sink := &usage.Sink{}
	calls := 0
	adapter := scriptAdapter{buildErr: &media.NotEntitled{Reason: "no account entitled to image generation"}}
	counting := table{"edge": countingAdapter{adapter, &calls}}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: edgeRegistry(t), Adapters: counting, Client: gatewaymedia.HTTP{},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    sink, Now: time.Now,
	})
	out := p.Images(context.Background(), gatewaymedia.Input{Path: "/v1/images/generations", Body: []byte(`{"model":"edge-img","prompt":"p"}`)})
	if out.Status != http.StatusForbidden || !strings.Contains(string(out.Body), "no account entitled") || calls != 2 {
		t.Fatalf("%d calls=%d %s", out.Status, calls, out.Body)
	}
	list, err := st.Connections().List(context.Background(), repository.ConnFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range list {
		if status, _ := c.Doc().GetString("testStatus"); status == "expired" {
			t.Fatalf("a plan refusal marked %s", c.ID)
		}
	}
}

func TestANoAuthServiceRunsEvenWhenEveryConnectionIsLocked(t *testing.T) {
	reg, err := upstream.Load(fstest.MapFS{"data/jina-ai.json": {Data: []byte(strings.ReplaceAll(noAuthEntry, "@", "https://x.example"))}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: table{"jina-ai": scriptAdapter{kind: upstream.KindEmbedding}},
		Client: gatewaymedia.HTTP{}, Selector: lockedSelector{},
		Reporter: account.NewReporter(nil, &tests.Log{}, nil), Usage: &usage.Sink{}, Now: time.Now,
	})
	out := p.Images(context.Background(), gatewaymedia.Input{Path: "/v1/images/generations", Body: []byte(`{"model":"local-embed","prompt":"p"}`)})
	if out.Status == http.StatusServiceUnavailable || out.Status == 0 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

type countingAdapter struct {
	inner media.Adapter
	n     *int
}

func (c countingAdapter) Kind() string { return c.inner.Kind() }
func (c countingAdapter) Build(ctx context.Context, r media.Request, cl media.Client) (media.Call, error) {
	*c.n++
	return c.inner.Build(ctx, r, cl)
}
func (c countingAdapter) Parse(ctx context.Context, r media.Request, cl media.Client, s int, h http.Header, b []byte) (media.Response, error) {
	return c.inner.Parse(ctx, r, cl, s, h, b)
}

type lockedSelector struct{}

func (lockedSelector) Select(context.Context, account.Request) (account.Selection, error) {
	return account.Selection{}, &account.AllLockedError{RetryAt: time.Now().Add(time.Minute)}
}

func TestAFixedRefusalWordReachesTheClient(t *testing.T) {
	st := twoAccounts(t)
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: edgeRegistry(t),
		Adapters: table{"edge": scriptAdapter{kind: upstream.KindImage, buildErr: &media.ProviderError{Status: http.StatusBadRequest, Text: "this model needs a reference image"}}},
		Client:   gatewaymedia.HTTP{},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    &usage.Sink{}, Now: time.Now,
	})
	out := p.Images(context.Background(), gatewaymedia.Input{Path: "/v1/images/generations", Body: []byte(`{"model":"edge-img","prompt":"p"}`)})
	if out.Status != http.StatusBadRequest || !strings.Contains(string(out.Body), "this model needs a reference image") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}
