package media_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	"rllmx/app/llm/media"
	mediamux "rllmx/app/llm/media/mux"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

const videoEntry = `{"id":"xai","category":"apikey","display":{"name":"xAI"},
"auth":{"scheme":"bearer","header":"Authorization"},
"models":[{"id":"grok-4"},{"id":"grok-imagine-video","kind":"video"}],
"media":{"serviceKinds":["llm","video"],
 "videoConfig":{"baseUrl":"@/v1/videos","authType":"apikey","authHeader":"bearer"}}}`

const chatEntry = `{"id":"openai","category":"apikey","display":{"name":"OpenAI"},
"auth":{"scheme":"bearer","header":"Authorization"},
"transport":{"baseUrl":"https://api.openai.test/v1","format":"openai"},
"models":[{"id":"gpt-4o"}]}`

// videoRig builds a pipeline whose one server answers the video entry, with one
// connection per key in the order given.
func videoRig(t *testing.T, handler http.HandlerFunc, keys ...string) (*gatewaymedia.Pipeline, *usage.Sink, *httptest.Server) {
	t.Helper()
	p, sink, _, srv := videoRigStore(t, handler, keys...)
	return p, sink, srv
}

// videoRigWithStore is videoRig that also hands back the connections, for a
// test that reads what a request wrote to them.
func videoRigWithStore(t *testing.T, handler http.HandlerFunc, keys ...string) (*gatewaymedia.Pipeline, *usage.Sink, *repository.ConnectionsRepo) {
	t.Helper()
	p, sink, conns, _ := videoRigStore(t, handler, keys...)
	return p, sink, conns
}

func videoRigStore(t *testing.T, handler http.HandlerFunc, keys ...string) (*gatewaymedia.Pipeline, *usage.Sink, *repository.ConnectionsRepo, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	reg, err := upstream.Load(fstest.MapFS{
		"data/xai.json":    {Data: []byte(strings.ReplaceAll(videoEntry, "@", srv.URL))},
		"data/openai.json": {Data: []byte(chatEntry)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	for _, k := range keys {
		if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
			Provider: "xai", AuthType: "apikey", Name: k, Fields: map[string]string{"apiKey": `"` + k + `"`},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	sink := &usage.Sink{}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Usage:    sink, Now: time.Now,
		Connections: st.Connections(),
	})
	return p, sink, st.Connections(), srv
}

// videoCreate is the client's body for a create.
func videoCreate(model string) gatewaymedia.Input {
	return gatewaymedia.Input{
		Path: "/v1/videos/generations", ContentType: "application/json",
		Body: []byte(`{"model":"` + model + `","prompt":"a cat"}`),
	}
}

func TestAVideoCreateAnswersThePinnedAccount(t *testing.T) {
	var got string
	p, sink, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = r.URL.Path + " " + string(body)
		_, _ = w.Write([]byte(`{"request_id":"r1","status":"pending"}`))
	}, "KEY")
	out := p.Videos(context.Background(), videoCreate("xai/grok-imagine-video"), media.VideoGenerations)
	if out.Status != 200 || gjson.GetBytes(out.Body, "request_id").String() != "r1" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if got != `/v1/videos/generations {"model":"grok-imagine-video","prompt":"a cat"}` {
		t.Fatalf("%s", got)
	}
	if out.Header["x-9router-connection-id"] == "" || out.Header["X-Rllm-Connection-Id"] == "" {
		t.Fatalf("%v", out.Header)
	}
	rows := sink.All()
	if len(rows) != 1 || rows[0].Endpoint != "/v1/videos/generations" || rows[0].Status != 200 {
		t.Fatalf("%+v", rows)
	}
}

func TestAVideoCreateRotatesOnAQuotaRefusal(t *testing.T) {
	var calls int
	p, sink, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") == "Bearer FIRST" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"request_id":"r1"}`))
	}, "FIRST", "SECOND")
	out := p.Videos(context.Background(), videoCreate("grok-imagine-video"), media.VideoGenerations)
	if out.Status != 200 || calls != 2 {
		t.Fatalf("%d calls=%d %s", out.Status, calls, out.Body)
	}
	if len(sink.All()) != 1 {
		t.Fatalf("%+v", sink.All())
	}
}

func TestAVideoCreateIsNotSentAgainAfterAServerError(t *testing.T) {
	var calls int
	p, _, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}, "FIRST", "SECOND")
	out := p.Videos(context.Background(), videoCreate("grok-imagine-video"), media.VideoGenerations)
	if calls != 1 {
		t.Fatalf("the create was sent %d times", calls)
	}
	if out.Status != http.StatusInternalServerError {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAVideoCreateTimesOutOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()
	reg, err := upstream.Load(fstest.MapFS{"data/xai.json": {Data: []byte(strings.ReplaceAll(videoEntry, "@", srv.URL))}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "xai", AuthType: "apikey", Name: "K", Fields: map[string]string{"apiKey": `"KEY"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector:     account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter:     account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Now:          time.Now,
		VideoTimeout: 20 * time.Millisecond,
	})
	out := p.Videos(context.Background(), videoCreate("grok-imagine-video"), media.VideoGenerations)
	if calls.Load() != 1 || out.Status != http.StatusGatewayTimeout {
		t.Fatalf("%d calls=%d %s", calls.Load(), out.Status, out.Body)
	}
}

func TestAMultipartCreateIsForwardedUnchanged(t *testing.T) {
	var gotBody, gotType string
	p, _, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotType = string(body), r.Header.Get("Content-Type")
		_, _ = w.Write([]byte(`{"request_id":"r1"}`))
	}, "KEY")
	raw := "--abc\r\nContent-Disposition: form-data; name=\"prompt\"\r\n\r\na cat\r\n--abc--"
	in := gatewaymedia.Input{Path: "/v1/videos/edits", ContentType: "multipart/form-data; boundary=abc", Body: []byte(raw)}
	out := p.Videos(context.Background(), in, media.VideoEdits)
	if out.Status != 200 || gotBody != raw || gotType != in.ContentType {
		t.Fatalf("%d %s %q", out.Status, gotBody, gotType)
	}
}

func TestABareModelGoesToTheDefaultProvider(t *testing.T) {
	var path string
	p, _, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"request_id":"r1"}`))
	}, "KEY")
	// A bare id, even one another provider owns, is not resolved here.
	out := p.Videos(context.Background(), videoCreate("gpt-4o"), media.VideoGenerations)
	if out.Status != 200 || path != "/v1/videos/generations" {
		t.Fatalf("%d %s %s", out.Status, path, out.Body)
	}
}

func TestAPrefixedChatProviderIsRefused(t *testing.T) {
	var calls int
	p, _, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) { calls++ }, "KEY")
	out := p.Videos(context.Background(), videoCreate("openai/gpt-4o"), media.VideoGenerations)
	if out.Status != http.StatusBadRequest || calls != 0 {
		t.Fatalf("%d calls=%d %s", out.Status, calls, out.Body)
	}
}

func TestAPollReadsThePinnedAccount(t *testing.T) {
	var path string
	p, sink, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"request_id":"r1","status":"completed"}`))
	}, "KEY")
	out := p.VideoJob(context.Background(), gatewaymedia.Input{Path: "/v1/videos/r1"}, "r1")
	if out.Status != 200 || path != "/v1/videos/r1" {
		t.Fatalf("%d %s %s", out.Status, path, out.Body)
	}
	if out.Header["x-9router-connection-id"] == "" {
		t.Fatalf("%v", out.Header)
	}
	if len(sink.All()) != 0 {
		t.Fatalf("a poll wrote a usage row: %+v", sink.All())
	}
}

func TestAPollIgnoresAQueryProviderWithoutVideo(t *testing.T) {
	var path string
	p, _, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"request_id":"r1"}`))
	}, "KEY")
	// The reference falls back to its default provider when the named one serves
	// no video.
	out := p.VideoJob(context.Background(), gatewaymedia.Input{Path: "/v1/videos/r1", Provider: "openai"}, "r1")
	if out.Status != 200 || path != "/v1/videos/r1" {
		t.Fatalf("%d %s %s", out.Status, path, out.Body)
	}
}

func TestAVideoJobNeedsAnID(t *testing.T) {
	p, _, _ := videoRig(t, func(w http.ResponseWriter, r *http.Request) {}, "KEY")
	if out := p.VideoJob(context.Background(), gatewaymedia.Input{Path: "/v1/videos/"}, ""); out.Status != http.StatusBadRequest {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAnImagesRequestReachesAProvidersVideoModel(t *testing.T) {
	var submit string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"status":"SUCCEEDED","output":["https://img/1.png"]}`))
			return
		}
		submit = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"t1"}`))
	}))
	defer srv.Close()
	entry := `{"id":"runwayml","category":"apikey","display":{"name":"Runway"},
"auth":{"scheme":"bearer","header":"Authorization"},
"media":{"serviceKinds":["image"],"imageConfig":{"baseUrl":"@","authType":"apikey","authHeader":"bearer"}},
"models":[{"id":"gen4_image"},{"id":"gen4_turbo","kind":"video"}]}`
	reg, err := upstream.Load(fstest.MapFS{"data/runwayml.json": {Data: []byte(strings.ReplaceAll(entry, "@", srv.URL))}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "runwayml", AuthType: "apikey", Name: "K", Fields: map[string]string{"apiKey": `"KEY"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: mediamux.New(mediamux.Options{}), Client: gatewaymedia.HTTP{Client: srv.Client()},
		Selector: account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter: account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Now:      time.Now,
	})
	out := p.Images(context.Background(), gatewaymedia.Input{
		Path: "/v1/images/generations", Body: []byte(`{"model":"runwayml/gen4_turbo","prompt":"hi"}`),
	})
	if out.Status != 200 || gjson.GetBytes(out.Body, "data.0.url").String() != "https://img/1.png" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if submit != "/image_to_video" {
		t.Fatalf("submit reached %s", submit)
	}
}

// locked reads the lock keys a connection carries.
func locked(t *testing.T, st *repository.ConnectionsRepo) []string {
	t.Helper()
	all, err := st.List(context.Background(), repository.ConnFilter{})
	if err != nil || len(all) == 0 {
		t.Fatalf("connections: %v (%d)", err, len(all))
	}
	var keys []string
	d := all[0].Doc()
	for _, k := range d.Keys() {
		if strings.HasPrefix(k, repository.LockPrefix) {
			if raw, _ := d.GetRaw(k); raw != "" && raw != "null" {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

func TestAnUnknownVideoJobNeverLocksTheAccountForChat(t *testing.T) {
	p, _, conns := videoRigWithStore(t, func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}, "KEY")
	for i := 0; i < 3; i++ {
		out := p.VideoJob(context.Background(), gatewaymedia.Input{Path: "/v1/videos/ghost"}, "ghost")
		if out.Status != http.StatusNotFound {
			t.Fatalf("poll %d: %d %s", i, out.Status, out.Body)
		}
	}
	if keys := locked(t, conns); len(keys) != 0 {
		t.Fatalf("a poll locked the account: %v", keys)
	}
}

func TestASlowSuccessDoesNotClearAFailureThatLandedMeanwhile(t *testing.T) {
	var conns *repository.ConnectionsRepo
	var model string
	p, _, conns := videoRigWithStore(t, func(w http.ResponseWriter, r *http.Request) {
		// While this call is in flight, a concurrent request on the same account
		// is refused with a rate limit, which locks the model.
		all, err := conns.List(context.Background(), repository.ConnFilter{})
		if err != nil || len(all) == 0 {
			t.Errorf("connections: %v", err)
			return
		}
		rep := account.NewReporter(conns, &tests.Log{}, nil)
		if _, err := rep.Fail(context.Background(), account.FailInput{ConnID: all[0].ID, Provider: "xai", Model: model, Status: http.StatusTooManyRequests}); err != nil {
			t.Errorf("fail: %v", err)
		}
		_, _ = w.Write([]byte(`{"request_id":"r1"}`))
	}, "KEY")
	model = "grok-imagine-video"
	out := p.Videos(context.Background(), videoCreate("xai/grok-imagine-video"), media.VideoGenerations)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if keys := locked(t, conns); len(keys) == 0 {
		t.Fatal("an older success cleared a failure that was recorded after it began")
	}
}
