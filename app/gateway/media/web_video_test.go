package media_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/gateway/account"
	gatewaymedia "rllmx/app/gateway/media"
	"rllmx/app/llm/media"
	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
	"rllmx/tests/usage"
)

const webVideoEntry = `{"id":"uc-web","category":"apikey","display":{"name":"UC"},
"models":[{"id":"default","kind":"video"}],
"media":{"serviceKinds":["video"],"noAuth":true,
 "videoConfig":{"baseUrl":"https://internal.example","authType":"none","executorDriven":true}}}`

// doneDirector answers a completed generation, the way a web session's video
// create does.
type doneDirector struct{}

func (doneDirector) Kind() string { return upstream.KindVideo }

func (doneDirector) Direct(context.Context, media.Request, media.Client) (media.Response, error) {
	return media.Response{Status: 200, Header: map[string]string{"Content-Type": "application/json"},
		Body: []byte(`{"id":"video-uc-web-1","status":"completed","video":{"url":"https://cdn.example/v.mp4"}}`)}, nil
}

type doneTable struct{}

func (doneTable) For(provider, kind, _ string) (media.Handler, bool) {
	if provider == "uc-web" && kind == upstream.KindVideo {
		return doneDirector{}, true
	}
	return nil, false
}

func webVideoPipeline(t *testing.T) *gatewaymedia.Pipeline {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{"data/uc-web.json": {Data: []byte(webVideoEntry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: doneTable{}, Client: gatewaymedia.HTTP{},
		Usage: &usage.Sink{}, Now: time.Now,
	})
}

func TestAWebVideoCreateAnswersCompleted(t *testing.T) {
	p := webVideoPipeline(t)
	out := p.Videos(context.Background(), gatewaymedia.Input{
		Path: "/v1/videos/generations", ContentType: "application/json",
		Body: []byte(`{"model":"uc-web/default","prompt":"a wave"}`),
	}, media.VideoGenerations)
	if out.Status != 200 || !strings.Contains(string(out.Body), `"status":"completed"`) {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAWebVideoPollNamesNoJob(t *testing.T) {
	p := webVideoPipeline(t)
	out := p.VideoJob(context.Background(), gatewaymedia.Input{
		Path: "/v1/videos/video-uc-web-1", Provider: "uc-web",
	}, "video-uc-web-1")
	if out.Status != http.StatusNotFound {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if !strings.Contains(string(out.Body), "No provider serves that video job") {
		t.Fatalf("body = %s", out.Body)
	}
}

// rotatingDirector hands back a fresh cookie and then fails, the way a web
// session does when its render is refused after the session was minted.
type rotatingDirector struct{ err error }

func (rotatingDirector) Kind() string { return upstream.KindVideo }

func (d rotatingDirector) Direct(context.Context, media.Request, media.Client) (media.Response, error) {
	return media.Response{NewCredential: "__client=rotated"}, d.err
}

type rotatingTable struct{ d rotatingDirector }

func (t rotatingTable) For(provider, kind, _ string) (media.Handler, bool) {
	if provider == "uc-web" && kind == upstream.KindVideo {
		return t.d, true
	}
	return nil, false
}

// savedCredential records what the pipeline asked the store to keep.
type savedCredential struct{ saved []string }

func (s *savedCredential) UpdateApiKey(_ context.Context, _, key string) error {
	s.saved = append(s.saved, key)
	return nil
}

func TestARotatedCookieIsKeptEvenWhenTheRenderFails(t *testing.T) {
	const cookieEntry = `{"id":"uc-web","category":"webCookie","display":{"name":"UC"},
"models":[{"id":"default","kind":"video"}],
"media":{"serviceKinds":["video"],
 "videoConfig":{"executorDriven":true,"baseUrl":"https://internal.example","authType":"cookie"}}}`
	reg, err := upstream.Load(fstest.MapFS{"data/uc-web.json": {Data: []byte(cookieEntry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	for name, failure := range map[string]error{
		"a refused render": &media.ProviderError{Status: http.StatusTooManyRequests, Text: "spent"},
		"a bad request":    media.BadRequestf("no"),
		"a plain failure":  errors.New("network"),
	} {
		t.Run(name, func(t *testing.T) {
			st := store.Migrated(t)
			if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
				Provider: "uc-web", AuthType: "cookie", Name: "c", Fields: map[string]string{"apiKey": `"__client=old"`},
			}, nil); err != nil {
				t.Fatal(err)
			}
			saved := &savedCredential{}
			p := gatewaymedia.New(gatewaymedia.Deps{
				Registry: reg, Adapters: rotatingTable{rotatingDirector{failure}}, Client: gatewaymedia.HTTP{},
				Selector:    account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
				Reporter:    account.NewReporter(st.Connections(), &tests.Log{}, nil),
				Credentials: saved, Usage: &usage.Sink{}, Now: time.Now,
			})
			p.Videos(context.Background(), gatewaymedia.Input{
				Path: "/v1/videos/generations", ContentType: "application/json",
				Body: []byte(`{"model":"uc-web/default","prompt":"a wave"}`),
			}, media.VideoGenerations)
			if len(saved.saved) != 1 || saved.saved[0] != "__client=rotated" {
				t.Fatalf("saved = %v, want the rotated cookie once", saved.saved)
			}
		})
	}
}

// embedDirector answers an embeddings call, so a test reaches the account loop.
type embedDirector struct{}

func (embedDirector) Kind() string { return upstream.KindEmbedding }

func (embedDirector) Direct(context.Context, media.Request, media.Client) (media.Response, error) {
	return media.Response{Status: 200, Body: []byte(`{"data":[]}`)}, nil
}

type embedTable struct{}

func (embedTable) For(provider, kind, _ string) (media.Handler, bool) {
	if provider == "github" && kind == upstream.KindEmbedding {
		return embedDirector{}, true
	}
	return nil, false
}

// mintFake is a minter and a refresher whose calls fail the way a test says.
type mintFake struct{ mintErr, readyErr error }

func (m mintFake) Token(context.Context, repository.Connection) (string, error) {
	return "minted", m.mintErr
}

func (m mintFake) Renew(context.Context, repository.Connection, string) (string, error) {
	return "", errors.New("no renewal")
}

func (m mintFake) Ready(_ context.Context, c repository.Connection) (repository.Connection, error) {
	return c, m.readyErr
}

func (m mintFake) RenewConn(_ context.Context, c repository.Connection) (repository.Connection, error) {
	return c, errors.New("no renewal")
}

// refresher adapts mintFake to the pipeline's Refresher, which names Renew too.
type refresher struct{ mintFake }

func (r refresher) Renew(ctx context.Context, c repository.Connection) (repository.Connection, error) {
	return r.RenewConn(ctx, c)
}

const mintEntry = `{"id":"github","category":"oauth","display":{"name":"G"},
"models":[{"id":"m","kind":"embedding"}],
"oauth":{"flow":"device","deviceUrl":"https://github.test/device","tokenUrl":"https://github.test/token","scope":"read:user","mint":"copilot",
 "endpoints":{"copilotToken":"https://api.github.test/copilot"}},
"media":{"serviceKinds":["embedding"],"embeddingConfig":{"baseUrl":"https://api.example/v1/embeddings","authType":"apikey"}}}`

// mintRun sends one embeddings call through an OAuth account whose token calls
// fail as given, and returns the answer and the lock keys the account carries.
func mintRun(t *testing.T, f mintFake) (gatewaymedia.Output, []string) {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{"data/github.json": {Data: []byte(mintEntry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	st := store.Migrated(t)
	if _, err := st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "github", AuthType: "oauth", Name: "g", Fields: map[string]string{"accessToken": `"tok"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	p := gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: embedTable{}, Client: gatewaymedia.HTTP{},
		Selector:  account.NewSelector(account.SelectorOptions{Conns: st.Connections(), Settings: st.Settings()}),
		Reporter:  account.NewReporter(st.Connections(), &tests.Log{}, nil),
		Refresher: refresher{f}, Minter: f, Usage: &usage.Sink{}, Now: time.Now,
	})
	out := p.Embeddings(context.Background(), gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"model":"github/m","input":"hi"}`)})
	return out, locked(t, st.Connections())
}

func TestATokenServiceThatIsDownDoesNotLockTheAccount(t *testing.T) {
	out, keys := mintRun(t, mintFake{mintErr: &oauth.Error{Class: oauth.ClassTransport}})
	if out.Status != http.StatusBadGateway {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if len(keys) != 0 {
		t.Fatalf("a token-service outage locked the account: %v", keys)
	}
}

func TestARefusedTokenLocksTheAccount(t *testing.T) {
	_, keys := mintRun(t, mintFake{mintErr: &oauth.Error{Class: oauth.ClassRejected}})
	if len(keys) == 0 {
		t.Fatal("a refused token left the account unlocked")
	}
}

func TestAReadinessBlipGoesOnWithTheTokenItHas(t *testing.T) {
	out, keys := mintRun(t, mintFake{readyErr: &oauth.Error{Class: oauth.ClassTransport}})
	if out.Status != 200 || len(keys) != 0 {
		t.Fatalf("a readiness blip stopped the call (%d) or locked the account (%v)", out.Status, keys)
	}
}

func TestADeadLoginLocksTheAccount(t *testing.T) {
	_, keys := mintRun(t, mintFake{readyErr: oauth.ErrExpired})
	if len(keys) == 0 {
		t.Fatal("a login that needs reconnecting left the account unlocked")
	}
}

// slowDirector waits for its context, the way a poll loop does.
type slowDirector struct{}

func (slowDirector) Kind() string { return upstream.KindVideo }

func (slowDirector) Direct(ctx context.Context, _ media.Request, _ media.Client) (media.Response, error) {
	<-ctx.Done()
	return media.Response{}, ctx.Err()
}

type slowTable struct{}

func (slowTable) For(provider, kind, _ string) (media.Handler, bool) {
	if provider == "uc-web" && kind == upstream.KindVideo {
		return slowDirector{}, true
	}
	return nil, false
}

func slowPipeline(t *testing.T, timeout time.Duration) (*gatewaymedia.Pipeline, *usage.Sink) {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{"data/uc-web.json": {Data: []byte(webVideoEntry)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	sink := &usage.Sink{}
	return gatewaymedia.New(gatewaymedia.Deps{
		Registry: reg, Adapters: slowTable{}, Client: gatewaymedia.HTTP{},
		Usage: sink, Now: time.Now, VideoTimeout: timeout,
	}), sink
}

func TestASlowRenderIsAGatewayTimeout(t *testing.T) {
	p, sink := slowPipeline(t, 30*time.Millisecond)
	// The data file's own timeout wins over the deps bound, so the entry carries none.
	out := p.Videos(context.Background(), gatewaymedia.Input{
		Path: "/v1/videos/generations", ContentType: "application/json",
		Body: []byte(`{"model":"uc-web/default","prompt":"a wave"}`),
	}, media.VideoGenerations)
	if out.Status != http.StatusGatewayTimeout {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if rows := sink.All(); len(rows) != 1 || rows[0].ErrorClass != "timeout" {
		t.Fatalf("%+v", rows)
	}
}

func TestAGoneClientIsRecordedAsClosedNotAsANetworkFault(t *testing.T) {
	p, sink := slowPipeline(t, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(30*time.Millisecond, cancel)
	out := p.Videos(ctx, gatewaymedia.Input{
		Path: "/v1/videos/generations", ContentType: "application/json",
		Body: []byte(`{"model":"uc-web/default","prompt":"a wave"}`),
	}, media.VideoGenerations)
	if out.Status != 499 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if rows := sink.All(); len(rows) != 1 || rows[0].ErrorClass != "client_closed" {
		t.Fatalf("%+v", rows)
	}
}

func TestEveryEarlyRefusalLeavesOneUsageRow(t *testing.T) {
	ctx := context.Background()
	for name, call := range map[string]func(p *gatewaymedia.Pipeline) gatewaymedia.Output{
		"embeddings, not json": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Embeddings(ctx, gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte("nope")})
		},
		"embeddings, no model": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Embeddings(ctx, gatewaymedia.Input{Path: "/v1/embeddings", Body: []byte(`{"input":"x"}`)})
		},
		"images, not json": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Images(ctx, gatewaymedia.Input{Path: "/v1/images/generations", Body: []byte("nope")})
		},
		"speech, not json": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Speech(ctx, gatewaymedia.Input{Path: "/v1/audio/speech", Body: []byte("nope")})
		},
		"transcription, no file": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Transcriptions(ctx, gatewaymedia.Input{Path: "/v1/audio/transcriptions"})
		},
		"transcription, no model": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Transcriptions(ctx, gatewaymedia.Input{Path: "/v1/audio/transcriptions", Form: &gatewaymedia.Form{Audio: []byte("a")}})
		},
		"video, no action": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Videos(ctx, gatewaymedia.Input{Path: "/v1/videos/x"}, "bogus")
		},
		"video, a provider with no video": func(p *gatewaymedia.Pipeline) gatewaymedia.Output {
			return p.Videos(ctx, gatewaymedia.Input{Path: "/v1/videos/generations", ContentType: "application/json",
				Body: []byte(`{"model":"nowhere/x","prompt":"p"}`)}, media.VideoGenerations)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, sink := slowPipeline(t, time.Minute)
			out := call(p)
			if out.Status < 400 || out.Status > 499 {
				t.Fatalf("status %d, want a refusal: %s", out.Status, out.Body)
			}
			rows := sink.All()
			if len(rows) != 1 || rows[0].Status != out.Status || rows[0].ErrorClass == "" || rows[0].ErrorClass == "none" {
				t.Fatalf("rows = %+v for a %d answer", rows, out.Status)
			}
		})
	}
}
