package media_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	gatewaymedia "rllmx/app/gateway/media"
	"rllmx/app/llm/media"
	"rllmx/app/llm/upstream"
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
