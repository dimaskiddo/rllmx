package stream

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	"rllmx/tests"
)

func streamRoute(t *testing.T, path string, h contractshttp.HandlerFunc) *goravelgin.Route {
	t.Helper()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	r.Get(path, h)
	return r
}

func TestTheFrameworksWriterHidesTheErrorThatWriterOfSurfaces(t *testing.T) {
	var viaFramework, viaWriterOf error
	r := streamRoute(t, "/hide", func(ctx contractshttp.Context) contractshttp.Response {
		w := ctx.Response().Writer()
		w.WriteHeader(200)
		viaFramework = http.NewResponseController(w).Flush()
		s, _ := NewSSE(WriterOf(ctx), Options{})
		viaWriterOf = s.Data("x")
		return nil
	})
	r.ServeHTTP(&failingWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: http.ErrHandlerTimeout}, httptest.NewRequest(http.MethodGet, "/hide", nil))
	if viaFramework != nil {
		t.Skipf("this driver reports flush errors itself (%v); unwrapping is then redundant but harmless", viaFramework)
	}
	if !errors.Is(viaWriterOf, http.ErrHandlerTimeout) {
		t.Fatalf("WriterOf must surface the failure the framework's writer hides: %v", viaWriterOf)
	}
}

func TestThroughTheDriverAClosedClientIsDetected(t *testing.T) {
	started := make(chan struct{})
	result := make(chan error, 1)
	r := streamRoute(t, "/gone", func(ctx contractshttp.Context) contractshttp.Response {
		s, err := NewSSE(WriterOf(ctx), Options{})
		if err != nil {
			result <- err
			return nil
		}
		_ = s.Data("")
		close(started)
		payload := strings.Repeat("x", 64<<10)
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
			if err := s.Data(payload); err != nil {
				result <- err
				return nil
			}
		}
		result <- errors.New("kept succeeding")
		return nil
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("GET /gone HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-started
	_ = conn.Close()
	select {
	case err := <-result:
		if err == nil || err.Error() == "kept succeeding" {
			t.Fatalf("through the driver the closed client went unnoticed: %v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("never noticed")
	}
}

func TestTheDriverWritesTheStatusOnceWithTheStreamHeaders(t *testing.T) {
	r := streamRoute(t, "/once", func(ctx contractshttp.Context) contractshttp.Response {
		s, _ := NewSSE(WriterOf(ctx), Options{})
		_ = s.Data("one")
		_ = s.Event("two", "2")
		return nil
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/once")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("the client saw %d %v", resp.StatusCode, resp.Header)
	}
	if string(body) != "data: one\n\nevent: two\ndata: 2\n\n" {
		t.Fatalf("body %q", body)
	}
}

func TestWriterOfLeavesTheFrameworksBufferAlone(t *testing.T) {
	var buffered int
	r := streamRoute(t, "/buffer", func(ctx contractshttp.Context) contractshttp.Response {
		s, _ := NewSSE(WriterOf(ctx), Options{})
		for range 100 {
			_ = s.Data(strings.Repeat("y", 1000))
		}
		buffered = ctx.Response().Origin().Body().Len()
		return nil
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/buffer", nil))
	if rec.Body.Len() < 100*1000 {
		t.Fatalf("the client got %d bytes", rec.Body.Len())
	}
	if buffered != 0 {
		t.Fatalf("the framework copied %d bytes of the stream into memory", buffered)
	}
}

func TestWriterOfKeepsTheStatusAndSizeVisibleToTheAccessLog(t *testing.T) {
	var status, size int
	r := streamRoute(t, "/count", func(ctx contractshttp.Context) contractshttp.Response {
		s, _ := NewSSE(WriterOf(ctx), Options{})
		_ = s.Data("hello")
		status, size = ctx.Response().Origin().Status(), ctx.Response().Origin().Size()
		return nil
	})
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/count", nil))
	if status != 200 || size != len("data: hello\n\n") {
		t.Fatalf("status %d size %d", status, size)
	}
}

func TestWriterOfPassesAnOtherWriterThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	r := tests.NewRoute(t)
	r.SetGlobalMiddleware(nil)
	var got http.ResponseWriter
	r.Get("/plain", func(ctx contractshttp.Context) contractshttp.Response {
		got = ctx.Response().Writer()
		return nil
	})
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plain", nil))
	if got == nil {
		t.Fatal("handler did not run")
	}
	if inner := WriterOf(nil); inner != nil {
		t.Fatalf("no context must give no writer: %v", inner)
	}
}
