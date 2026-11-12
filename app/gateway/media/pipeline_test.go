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
	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

type rig struct {
	p      *gatewaymedia.Pipeline
	sink   *usage.Sink
	server *httptest.Server
}

func newRig(t *testing.T, entry string) *rig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/embeddings":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`))
		case "/v1/images/generations":
			_, _ = w.Write([]byte(`{"created":1,"data":[{"b64_json":"iVBORw0KGgo="}]}`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	entry = strings.ReplaceAll(entry, "@", srv.URL)
	reg, err := upstream.Load(fstest.MapFS{"data/openai.json": {Data: []byte(entry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"KEY"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sink := &usage.Sink{}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg,
		Adapters: mediamux.New(mediamux.Options{}),
		Client:   gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings(), Now: time.Now}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, func() time.Time { return time.Unix(1_700_000_000, 0) }),
		Usage:    sink,
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	return &rig{p: p, sink: sink, server: srv}
}

// last is the row the call left behind.
func (r *rig) last(t *testing.T) analytics.Record {
	t.Helper()
	all := r.sink.All()
	if len(all) != 1 {
		t.Fatalf("%d usage rows", len(all))
	}
	return all[0]
}

const mediaEntry = `{"id":"openai","category":"apikey","display":{"name":"M"},
"auth":{"scheme":"bearer","header":"Authorization"},
"media":{"serviceKinds":["embedding","image"],
  "embeddingConfig":{"baseUrl":"@/v1/embeddings","authType":"apikey","authHeader":"bearer","models":[{"id":"text-embedding-3-small","name":"Small"}]},
  "imageConfig":{"baseUrl":"@/v1/images/generations","authType":"apikey","authHeader":"bearer","models":[{"id":"gpt-image-1","name":"Image"}]}}}`

func TestEmbeddingsEndToEnd(t *testing.T) {
	r := newRig(t, mediaEntry)
	out := r.p.Embeddings(context.Background(), gatewaymedia.Input{
		Path: "/v1/embeddings", Body: []byte(`{"model":"text-embedding-3-small","input":"hi"}`),
	})
	if out.Status != 200 || gjson.GetBytes(out.Body, "data.0.embedding.0").Float() != 0.1 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	row := r.last(t)
	if row.Endpoint != "/v1/embeddings" || row.PromptTokens != 3 || row.CompletionTokens != 0 || row.Status != 200 {
		t.Fatalf("%+v", row)
	}
}

func TestImagesEndToEnd(t *testing.T) {
	r := newRig(t, mediaEntry)
	out := r.p.Images(context.Background(), gatewaymedia.Input{
		Path: "/v1/images/generations", Body: []byte(`{"model":"gpt-image-1","prompt":"a cat"}`),
	})
	if out.Status != 200 || gjson.GetBytes(out.Body, "data.0.b64_json").String() != "iVBORw0KGgo=" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(t); row.Endpoint != "/v1/images/generations" || row.Status != 200 {
		t.Fatalf("%+v", row)
	}
}

func TestAnImageModelOnTheEmbeddingsEndpointIsNotFound(t *testing.T) {
	r := newRig(t, mediaEntry)
	out := r.p.Embeddings(context.Background(), gatewaymedia.Input{
		Path: "/v1/embeddings", Body: []byte(`{"model":"gpt-image-1","input":"hi"}`),
	})
	if out.Status != http.StatusNotFound {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(t); row.Status != http.StatusNotFound || row.ErrorClass != "not_found" {
		t.Fatalf("%+v", row)
	}
}

func TestAMissingInputIsRefusedBeforeAnyCall(t *testing.T) {
	r := newRig(t, mediaEntry)
	out := r.p.Embeddings(context.Background(), gatewaymedia.Input{
		Path: "/v1/embeddings", Body: []byte(`{"model":"text-embedding-3-small"}`),
	})
	if out.Status != http.StatusBadRequest {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestABinaryImageAnswerCarriesItsBytes(t *testing.T) {
	r := newRig(t, mediaEntry)
	out := r.p.Images(context.Background(), gatewaymedia.Input{
		Path: "/v1/images/generations?response_format=binary", Binary: true,
		Body: []byte(`{"model":"gpt-image-1","prompt":"a cat"}`),
	})
	if out.Status != 200 || out.Header["Content-Type"] != "image/png" || !strings.HasPrefix(string(out.Body), "\x89PNG") {
		t.Fatalf("%d %v %.10q", out.Status, out.Header, out.Body)
	}
}

func TestImageBoundsAreRefusedBeforeACall(t *testing.T) {
	r := newRig(t, mediaEntry)
	for name, body := range map[string]string{
		"too many": `{"model":"gpt-image-1","prompt":"x","n":99}`,
		"a size":   `{"model":"gpt-image-1","prompt":"x","size":"lots"}`,
		"no n":     `{"model":"gpt-image-1","prompt":"x","n":"two"}`,
	} {
		if out := r.p.Images(context.Background(), gatewaymedia.Input{Path: "/v1/images/generations", Body: []byte(body)}); out.Status != http.StatusBadRequest {
			t.Errorf("%s: %d", name, out.Status)
		}
	}
}

func TestEmbeddingInputIsBounded(t *testing.T) {
	r := newRig(t, mediaEntry)
	big := strings.Repeat("a", 33<<10)
	if out := r.p.Embeddings(context.Background(), gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"model":"text-embedding-3-small","input":"` + big + `"}`)}); out.Status != http.StatusBadRequest {
		t.Fatalf("oversized input: %d", out.Status)
	}
	list := `{"model":"text-embedding-3-small","input":[` + strings.Repeat(`"a",`, 2048) + `"a"]}`
	if out := r.p.Embeddings(context.Background(), gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(list)}); out.Status != http.StatusBadRequest {
		t.Fatalf("too many inputs: %d", out.Status)
	}
}

func TestANoAuthServiceRunsWithoutAConnection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.5]}]}`))
	}))
	defer srv.Close()
	entry := strings.ReplaceAll(noAuthEntry, "@", srv.URL)
	reg, err := upstream.Load(fstest.MapFS{"data/jina-ai.json": {Data: []byte(entry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	sink := &usage.Sink{}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    sink, Now: time.Now,
	})
	out := p.Embeddings(context.Background(), gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"model":"local-embed","input":"hi"}`)})
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := sink.All(); len(row) != 1 || row[0].Status != 200 {
		t.Fatalf("%+v", row)
	}
}

const noAuthEntry = `{"id":"jina-ai","category":"free","display":{"name":"Local"},
"media":{"serviceKinds":["embedding"],"noAuth":true,"embeddingConfig":{"baseUrl":"@/v1/embeddings","authType":"none","models":[{"id":"local-embed"}]}}}`

// nodeStore is a Nodes dep with one endpoint.
type nodeStore struct{ base string }

func (n nodeStore) List(context.Context, string) ([]repository.Node, error) {
	return []repository.Node{{ID: "custom-embedding-abc", Type: upstream.NodeTypeEmbedding, Data: `{"prefix":"my-embed","baseUrl":"` + n.base + `"}`}}, nil
}

func TestAnEmbeddingNodeIsServed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.5]}]}`))
	}))
	defer srv.Close()
	reg, err := upstream.Load(fstest.MapFS{"data/openai.json": {Data: []byte(strings.ReplaceAll(mediaEntry, "@", srv.URL))}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "custom-embedding-abc", AuthType: "apikey", Name: "node", Fields: map[string]string{"apiKey": `"NODEKEY"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Nodes:    nodeStore{base: srv.URL + "/v1"}, Usage: &usage.Sink{}, Now: time.Now,
	})
	out := p.Embeddings(context.Background(), gatewaymedia.Input{
		Path: "/v1/embeddings", Body: []byte(`{"model":"my-embed/some-model","input":"hi"}`),
	})
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestARefusedKeyIsAnsweredAndRecorded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	entry := strings.ReplaceAll(mediaEntry, "@", srv.URL)
	reg, err := upstream.Load(fstest.MapFS{"data/openai.json": {Data: []byte(entry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"KEY"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sink := &usage.Sink{}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    sink, Now: time.Now,
	})
	out := p.Embeddings(context.Background(), gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"model":"text-embedding-3-small","input":"hi"}`)})
	if out.Status != http.StatusUnauthorized {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := sink.All(); len(row) != 1 || row[0].Status != http.StatusUnauthorized || row[0].ErrorClass != "auth" {
		t.Fatalf("%+v", row)
	}
}

func TestARefusalFallsBackToTheNextAccount(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer FIRST" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"object":"list","data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":1,"total_tokens":1}}`))
	}))
	defer srv.Close()
	entry := strings.ReplaceAll(mediaEntry, "@", srv.URL)
	reg, err := upstream.Load(fstest.MapFS{"data/openai.json": {Data: []byte(entry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	for _, k := range []string{"FIRST", "SECOND"} {
		if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
			Provider: "openai", AuthType: "apikey", Name: k, Fields: map[string]string{"apiKey": `"` + k + `"`},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    &usage.Sink{}, Now: time.Now,
	})
	out := p.Embeddings(context.Background(), gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"model":"text-embedding-3-small","input":"hi"}`)})
	if out.Status != 200 || calls != 2 {
		t.Fatalf("%d calls=%d %s", out.Status, calls, out.Body)
	}
}
