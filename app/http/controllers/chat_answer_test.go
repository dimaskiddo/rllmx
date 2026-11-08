package controllers

import (
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/pipeline"
	"rllmx/tests"
)

// bufferProbe reads how many bytes the driver copied into its own buffer once
// the handler returned.
type bufferProbe struct{ kept *int }

func (bufferProbe) Signature() string { return "test:buffer-probe" }
func (p bufferProbe) Handle(ctx contractshttp.Context) {
	ctx.Request().Next()
	*p.kept = ctx.Response().Origin().Body().Len()
}

func TestABufferedAnswerIsNotCopiedIntoTheDriversBuffer(t *testing.T) {
	r := newChatRig(t, 1<<20, tests.Reply{ContentType: "application/json", Body: chatReply})
	kept := -1
	route := tests.NewRoute(t)
	g := apphttp.NewGuard(keyed{}, true)
	route.SetGlobalMiddleware([]contractshttp.Middleware{
		&middleware.RequestId{}, bufferProbe{&kept}, &middleware.Guard{Guard: g},
	})
	c := &ChatController{Pipe: func() (*pipeline.Pipeline, error) { return r.pipe() }, Log: &tests.Log{}}
	g.Handle(route, apphttp.ClassLLM, nethttp.MethodPost, "/v1/chat/completions", c.Serve)

	req := httptest.NewRequest(nethttp.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"`+chatModel+`","messages":[{"role":"user","content":"hi"}]}`)).WithContext(context.Background())
	req.RemoteAddr = "203.0.113.9:5000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	route.ServeHTTP(rec, req)

	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"hello"`) {
		t.Fatalf("the client got %d %s", rec.Code, rec.Body.String())
	}
	if kept != 0 {
		t.Fatalf("the driver kept a %d byte copy of the answer", kept)
	}
}
