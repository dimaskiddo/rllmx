package middleware

import (
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
)

// bulk is larger than net/http's 4 KiB response buffer, so a write reaches the
// socket and a deadline can be seen failing it.
var bulk = make([]byte, 64<<10)

// lateWriter answers after d, writing a body big enough to hit the socket, and
// reports the write error on a channel so the test reads it without a race.
func lateWriter(d time.Duration) (contractshttp.HandlerFunc, <-chan error) {
	errs := make(chan error, 1)
	return func(ctx contractshttp.Context) contractshttp.Response {
		time.Sleep(d)
		_, err := ctx.Response().Writer().Write(bulk)
		errs <- err
		return nil
	}, errs
}

func TestDeadlinesCutASlowDashboardAnswer(t *testing.T) {
	handler, errs := lateWriter(80 * time.Millisecond)
	r := newRoute(t, &Deadlines{APIWrite: 20 * time.Millisecond})
	r.Get("/api/slow", handler)

	srv := httptest.NewServer(nethttp.HandlerFunc(r.ServeHTTP))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/api/slow")
	if err == nil {
		_, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	select {
	case writeErr := <-errs:
		if writeErr == nil {
			t.Fatalf("the write after the deadline succeeded: it never reached the socket (client err=%v)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never finished")
	}
}

func TestDeadlinesDoNotBoundAClientAPIAnswer(t *testing.T) {
	handler, errs := lateWriter(80 * time.Millisecond)
	r := newRoute(t, &Deadlines{APIWrite: 20 * time.Millisecond, ClientRead: 20 * time.Millisecond})
	r.Get("/v1/slow", handler)

	srv := httptest.NewServer(nethttp.HandlerFunc(r.ServeHTTP))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/v1/slow")
	if err != nil {
		t.Fatalf("a client API answer was cut: %v", err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	if writeErr := <-errs; writeErr != nil {
		t.Fatalf("the write failed: %v", writeErr)
	}
	if n != int64(len(bulk)) {
		t.Fatalf("the client read %d of %d bytes", n, len(bulk))
	}
}

func TestDeadlinesSetNothingTheWriterCannotCarry(t *testing.T) {
	if rec := getOnly(t, "/api/thing", &Deadlines{}, nil); rec.Code != 200 {
		t.Fatalf("a recorder without a deadline was refused: %d", rec.Code)
	}
}

func TestDeadlinesDoNotBoundALongDashboardCall(t *testing.T) {
	handler, errs := lateWriter(80 * time.Millisecond)
	r := newRoute(t, &Deadlines{APIWrite: 20 * time.Millisecond})
	for _, path := range []string{"/api/health/latency-stream", "/api/providers/test-batch", "/api/providers/x/test-models"} {
		r.Get(path, handler)
		srv := httptest.NewServer(nethttp.HandlerFunc(r.ServeHTTP))
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatalf("%s: a long call was cut: %v", path, err)
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		srv.Close()
		if writeErr := <-errs; writeErr != nil {
			t.Fatalf("%s: the write failed: %v", path, writeErr)
		}
		if n != int64(len(bulk)) {
			t.Fatalf("%s: the client read %d of %d bytes", path, n, len(bulk))
		}
	}
}

// TestABodylessDashboardReadIsNotCanceledByTheReadDeadline guards the request
// context of a GET: net/http watches the socket for a hangup while the handler
// runs, and a read deadline that expires on that watch cancels the context.
func TestABodylessDashboardReadIsNotCanceledByTheReadDeadline(t *testing.T) {
	canceled := make(chan error, 1)
	r := newRoute(t, &Deadlines{APIRead: 30 * time.Millisecond})
	r.Get("/api/usage/logs", func(ctx contractshttp.Context) contractshttp.Response {
		time.Sleep(120 * time.Millisecond)
		canceled <- ctx.Request().Origin().Context().Err()
		return ctx.Response().String(nethttp.StatusOK, "ok")
	})

	srv := httptest.NewServer(nethttp.HandlerFunc(r.ServeHTTP))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/usage/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case got := <-canceled:
		if got != nil {
			t.Fatalf("the request context was canceled while the handler ran: %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never finished")
	}
}

func TestAnUploadIsNotCanceledByTheReadDeadlineOnceItsBodyIsRead(t *testing.T) {
	canceled := make(chan error, 1)
	r := newRoute(t, &Deadlines{APIRead: 30 * time.Millisecond})
	r.Post("/api/providers/x/test-models", func(ctx contractshttp.Context) contractshttp.Response {
		_, _ = io.Copy(io.Discard, ctx.Request().Origin().Body)
		time.Sleep(120 * time.Millisecond)
		canceled <- ctx.Request().Origin().Context().Err()
		return ctx.Response().String(nethttp.StatusOK, "ok")
	})

	srv := httptest.NewServer(nethttp.HandlerFunc(r.ServeHTTP))
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/api/providers/x/test-models", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-canceled; got != nil {
		t.Fatalf("the request context was canceled after the body was read: %v", got)
	}
}

func TestAStalledUploadStillHitsTheReadDeadline(t *testing.T) {
	r := newRoute(t, &Deadlines{APIRead: 50 * time.Millisecond})
	done := make(chan error, 1)
	r.Post("/api/providers", func(ctx contractshttp.Context) contractshttp.Response {
		_, err := io.Copy(io.Discard, ctx.Request().Origin().Body)
		done <- err
		return ctx.Response().String(nethttp.StatusOK, "ok")
	})

	srv := httptest.NewServer(nethttp.HandlerFunc(r.ServeHTTP))
	defer srv.Close()
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte("{"))
		time.Sleep(400 * time.Millisecond)
		pw.Close()
	}()
	req, _ := nethttp.NewRequest(nethttp.MethodPost, srv.URL+"/api/providers", pr)
	if resp, err := srv.Client().Do(req); err == nil {
		resp.Body.Close()
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a body that stalled past the deadline was read to the end")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never finished")
	}
}
