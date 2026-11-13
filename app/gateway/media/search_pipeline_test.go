package media_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/network"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

// serviceRig builds a pipeline over one or more provider files, all pointing at
// one fake server, with the connections the test named.
type serviceRig struct {
	p      *gatewaymedia.Pipeline
	sink   *usage.Sink
	server *httptest.Server
	conns  *repository.ConnectionsRepo
}

type fakeServer struct {
	status int
	body   string
	calls  []string
	header http.Header
}

func newServiceRig(t *testing.T, f *fakeServer, allow *network.Allow, entries map[string]string, connections ...repository.ConnInput) *serviceRig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.Method+" "+r.URL.String())
		f.header = r.Header.Clone()
		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
		}
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	files := fstest.MapFS{}
	for id, entry := range entries {
		files["data/"+id+".json"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(entry, "@", srv.URL))}
	}
	reg, err := upstream.Load(files, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	for _, c := range connections {
		if _, err := st.Connections().Create(context.Background(), c, nil); err != nil {
			t.Fatal(err)
		}
	}
	sink := &usage.Sink{}
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg,
		Adapters: mediamux.New(mediamux.Options{}),
		Client:   gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings(), Now: now}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, now),
		Usage:    sink,
		Now:      now,
		Network:  network.Options{Allow: allow},
	})
	return &serviceRig{p: p, sink: sink, server: srv, conns: st.Connections()}
}

const serperEntry = `{"id":"serper","category":"apikey","display":{"name":"Serper"},
"media":{"serviceKinds":["webSearch"],
"searchConfig":{"baseUrl":"@","method":"POST","authType":"apikey","authHeader":"x-api-key","searchTypes":["web","news"],"defaultMaxResults":5,"maxMaxResults":20,"timeoutMs":10000}}}`

func serperKey() repository.ConnInput {
	return repository.ConnInput{Provider: "serper", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"KEY"`}}
}

func TestASearchIsSentAndItsResultsAreNormalized(t *testing.T) {
	f := &fakeServer{body: `{"organic":[{"title":"A","link":"https://example.com/a?x=1","snippet":"first"},{"title":"B","link":"https://example.com/b"}],"searchParameters":{"totalResults":42}}`}
	r := newServiceRig(t, f, nil, map[string]string{"serper": serperEntry}, serperKey())
	out := r.p.Search(context.Background(), gatewaymedia.Input{Path: "/v1/search", Body: []byte(`{"provider":"serper","query":"hello","max_results":5}`)})
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := gjson.GetBytes(out.Body, "results.0.url").String(); got != "https://example.com/a?x=1" {
		t.Fatalf("url %q", got)
	}
	if gjson.GetBytes(out.Body, "results.0.display_url").String() != "example.com/a" {
		t.Fatalf("display %s", out.Body)
	}
	if gjson.GetBytes(out.Body, "metrics.total_results_available").Int() != 42 {
		t.Fatalf("total %s", out.Body)
	}
	if f.header.Get("X-Api-Key") != "KEY" || !strings.HasSuffix(f.calls[0], "/search") {
		t.Fatalf("call %v header %v", f.calls, f.header)
	}
	row := r.sink.All()[0]
	if row.Endpoint != "/v1/search" || row.Model != "serper" || row.Provider != "serper" {
		t.Fatalf("row %+v", row)
	}
}

func TestASearchFailureLocksOnlyTheCapability(t *testing.T) {
	f := &fakeServer{status: http.StatusTooManyRequests, body: `{"message":"slow down"}`}
	r := newServiceRig(t, f, nil, map[string]string{"serper": serperEntry}, serperKey())
	out := r.p.Search(context.Background(), gatewaymedia.Input{Path: "/v1/search", Body: []byte(`{"provider":"serper","query":"hello"}`)})
	if out.Status != http.StatusTooManyRequests {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	conns, err := r.conns.List(context.Background(), repository.ConnFilter{})
	if err != nil || len(conns) != 1 {
		t.Fatalf("%v %d", err, len(conns))
	}
	now := time.Unix(1_700_000_000, 0)
	if !account.LockActive(conns[0].Doc(), "websearch:serper", now) {
		t.Fatal("the search scope was not locked")
	}
	if account.LockActive(conns[0].Doc(), "glm-5", now) || account.LockActive(conns[0].Doc(), "", now) {
		t.Fatal("a failed search locked chat")
	}
}

const searxngEntry = `{"id":"searxng","category":"freeTier","display":{"name":"SearXNG"},
"media":{"serviceKinds":["webSearch"],"noAuth":true,
"searchConfig":{"baseUrl":"searxng","method":"GET","authType":"none","authHeader":"none","searchTypes":["web"],"defaultMaxResults":5,"maxMaxResults":50,"timeoutMs":10000}}}`

func TestSearxngRunsOnlyWhenTheOperatorNamedIt(t *testing.T) {
	f := &fakeServer{body: `{"results":[{"title":"A","url":"https://example.com/a","content":"c"}]}`}
	r := newServiceRig(t, f, nil, map[string]string{"searxng": searxngEntry})
	out := r.p.Search(context.Background(), gatewaymedia.Input{Path: "/v1/search", Body: []byte(`{"provider":"searxng","query":"hello"}`)})
	if out.Status != http.StatusBadRequest {
		t.Fatalf("an unconfigured instance answered %d", out.Status)
	}
	if len(f.calls) != 0 {
		t.Fatal("an unconfigured instance was called")
	}

	r2 := newServiceRig(t, f, nil, map[string]string{"searxng": searxngEntry})
	r2.p = gatewaymedia.New(gatewaymedia.Deps{
		Registry: registryOf(t, map[string]string{"searxng": searxngEntry}),
		Adapters: mediamux.New(mediamux.Options{}),
		Client:   gatewaymedia.HTTP{Client: r2.server.Client()},
		Usage:    r2.sink, Now: time.Now, SearxngURL: r2.server.URL,
	})
	out = r2.p.Search(context.Background(), gatewaymedia.Input{Path: "/v1/search", Body: []byte(`{"provider":"searxng","query":"hello"}`)})
	if out.Status != 200 || !strings.HasSuffix(f.calls[len(f.calls)-1], "/search?categories=general&format=json&q=hello") {
		t.Fatalf("%d %s calls %v", out.Status, out.Body, f.calls)
	}
}

func registryOf(t *testing.T, entries map[string]string) *upstream.Registry {
	t.Helper()
	files := fstest.MapFS{}
	for id, entry := range entries {
		files["data/"+id+".json"] = &fstest.MapFile{Data: []byte(entry)}
	}
	reg, err := upstream.Load(files, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

const localFetchEntry = `{"id":"local-fetch","category":"freeTier","display":{"name":"Local"},
"media":{"serviceKinds":["webFetch"],"noAuth":true,
"fetchConfig":{"baseUrl":"local-fetch","authType":"none","formats":["markdown","text","html"],"maxCharacters":200000,"timeoutMs":30000}}}`

func TestAFetchRefusesAPrivateTargetBeforeAnyCall(t *testing.T) {
	f := &fakeServer{body: `<html><body><p>hi</p></body></html>`}
	r := newServiceRig(t, f, nil, map[string]string{"local-fetch": localFetchEntry})
	out := r.p.Fetch(context.Background(), gatewaymedia.Input{Path: "/v1/web/fetch", Body: []byte(`{"provider":"local-fetch","url":"http://127.0.0.1/page"}`)})
	if out.Status != http.StatusBadRequest || len(f.calls) != 0 {
		t.Fatalf("%d %s after %d calls", out.Status, out.Body, len(f.calls))
	}
}

func TestTheBuiltInReaderConvertsThePage(t *testing.T) {
	f := &fakeServer{body: `<html><head><title>Doc</title></head><body><h1>Title</h1><p>Hello <a href="/more">there</a>.</p><script>bad()</script></body></html>`}
	allow, err := network.ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	r := newServiceRig(t, f, allow, map[string]string{"local-fetch": localFetchEntry})
	page := r.server.URL + "/page"
	out := r.p.Fetch(context.Background(), gatewaymedia.Input{Path: "/v1/web/fetch", Body: []byte(`{"provider":"local-fetch","url":"` + page + `"}`)})
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got := gjson.GetBytes(out.Body, "title").String(); got != "Doc" {
		t.Fatalf("title %q", got)
	}
	text := gjson.GetBytes(out.Body, "content.text").String()
	if !strings.Contains(text, "# Title") || !strings.Contains(text, "[there]("+r.server.URL+"/more)") || strings.Contains(text, "bad()") {
		t.Fatalf("markdown:\n%s", text)
	}
	if gjson.GetBytes(out.Body, "content.format").String() != "markdown" {
		t.Fatalf("format %s", out.Body)
	}
}

func TestABorrowedConnectionServesTheSearch(t *testing.T) {
	entry := `{"id":"ollama-search","category":"apikey","display":{"name":"O"},
"media":{"serviceKinds":["webSearch"],"credentialFallback":"ollama",
"searchConfig":{"baseUrl":"@","method":"POST","authType":"apikey","authHeader":"bearer","searchTypes":["web"],"defaultMaxResults":5,"maxMaxResults":10,"timeoutMs":10000}}}`
	ollama := `{"id":"ollama","category":"freeTier","display":{"name":"Ollama"},"transport":{"baseUrl":"https://ollama.com/api/chat","format":"ollama"},
"auth":{"scheme":"bearer","header":"Authorization"}}`
	f := &fakeServer{body: `{"results":[{"title":"A","url":"https://example.com/a","content":"c"}]}`}
	r := newServiceRig(t, f, nil, map[string]string{"ollama-search": entry, "ollama": ollama},
		repository.ConnInput{Provider: "ollama", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"SHARED"`}})
	out := r.p.Search(context.Background(), gatewaymedia.Input{Path: "/v1/search", Body: []byte(`{"provider":"ollama-search","query":"hello"}`)})
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if f.header.Get("Authorization") != "Bearer SHARED" {
		t.Fatalf("header %v", f.header)
	}
}
