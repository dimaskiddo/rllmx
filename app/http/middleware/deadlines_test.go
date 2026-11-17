package middleware

import (
	"io"
	nethttp "net/http"
	"net/http/httptest"
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
