package http

import (
	"context"
	"errors"
	"io"
	"maps"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/settings"
	"rllmx/tests"
)

func serverSettings(t *testing.T, over map[string]any) *settings.Server {
	t.Helper()
	m := tests.ServerValues()
	maps.Copy(m, over)
	s, err := settings.LoadServer(tests.Config{Values: m})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func noRoutes() []contractshttp.Info { return nil }

// newServer builds a Server on a loopback listener the OS picked.
func newServer(t *testing.T, h nethttp.Handler, over map[string]any) (*Server, *tests.Log, string) {
	t.Helper()
	l := &tests.Log{}
	s := NewServer(serverSettings(t, over), h, NewGuard(nil, true), noRoutes, l)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.listen = func(string) (net.Listener, error) { return ln, nil }
	return s, l, "http://" + ln.Addr().String()
}

func runInBackground(s *Server) <-chan error {
	done := make(chan error, 1)
	go func() { done <- s.Run() }()
	return done
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTheServerIsARunnerWithItsOwnSignatureAndAnEarlyShutdown(t *testing.T) {
	s := NewServer(serverSettings(t, nil), nethttp.NotFoundHandler(), NewGuard(nil, true), noRoutes, &tests.Log{})
	if s.Signature() != "rllmx:http" || !s.ShouldRun() {
		t.Fatalf("signature %q ShouldRun %v", s.Signature(), s.ShouldRun())
	}
	if s.ShutdownPriority() >= 0 {
		t.Fatalf("priority %d: the server must stop before the runners that hold the stores (priority 0 and above)", s.ShutdownPriority())
	}
}

func TestTheHTTPServerCarriesTheConfiguredTimeoutsAndNoWholeRequestTimeout(t *testing.T) {
	s := NewServer(serverSettings(t, map[string]any{
		"http.server.read_header_timeout": "7s", "http.server.idle_timeout": "90s", "http.drivers.gin.header_limit": 32,
	}), nethttp.NotFoundHandler(), NewGuard(nil, true), noRoutes, &tests.Log{})
	srv := s.build()
	if srv.ReadHeaderTimeout != 7*time.Second || srv.IdleTimeout != 90*time.Second || srv.MaxHeaderBytes != 32<<10 {
		t.Fatalf("ReadHeaderTimeout %v IdleTimeout %v MaxHeaderBytes %d", srv.ReadHeaderTimeout, srv.IdleTimeout, srv.MaxHeaderBytes)
	}
	if srv.ReadTimeout != 0 || srv.WriteTimeout != 0 {
		t.Fatalf("ReadTimeout %v WriteTimeout %v: a stream runs for minutes", srv.ReadTimeout, srv.WriteTimeout)
	}
}

func TestTheServerServesTheHandlerAndStopsCleanly(t *testing.T) {
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		_, _ = io.WriteString(w, "hello "+r.URL.Path)
	}), nil)
	done := runInBackground(s)
	var body string
	waitFor(t, "the server to answer", func() bool {
		resp, err := nethttp.Get(base + "/x")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return true
	})
	if body != "hello /x" {
		t.Fatalf("body %q", body)
	}
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a normal stop is not an error: %v", err)
	}
}

func TestRunRefusesToStartWithARouteOutsideTheGuardTable(t *testing.T) {
	l := &tests.Log{}
	g := NewGuard(nil, true)
	infos := []contractshttp.Info{{Method: "GET|HEAD", Path: "/api/sneaky"}}
	s := NewServer(serverSettings(t, nil), nethttp.NotFoundHandler(), g, func() []contractshttp.Info { return infos }, l)
	listened := false
	s.listen = func(string) (net.Listener, error) { listened = true; return nil, errors.New("must not be reached") }
	err := s.Run()
	if err == nil || !strings.Contains(err.Error(), "GET /api/sneaky") || !strings.Contains(err.Error(), "HEAD /api/sneaky") {
		t.Fatalf("error = %v", err)
	}
	if listened {
		t.Fatal("the socket was opened before the route table was checked")
	}
}

func TestAPortInUseFailsRunWithTheAddress(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	host, port, _ := net.SplitHostPort(taken.Addr().String())
	s := NewServer(serverSettings(t, map[string]any{"http.host": host, "http.port": port}), nethttp.NotFoundHandler(), NewGuard(nil, true), noRoutes, &tests.Log{})
	err = s.Run()
	if err == nil || !strings.Contains(err.Error(), taken.Addr().String()) {
		t.Fatalf("error = %v", err)
	}
}

func TestTheListenAddressComesFromTheSettings(t *testing.T) {
	var got string
	s := NewServer(serverSettings(t, map[string]any{"http.host": "10.1.2.3", "http.port": "4000"}), nethttp.NotFoundHandler(), NewGuard(nil, true), noRoutes, &tests.Log{})
	s.listen = func(addr string) (net.Listener, error) { got = addr; return nil, errors.New("stop") }
	_ = s.Run()
	if got != "10.1.2.3:4000" {
		t.Fatalf("addr %q", got)
	}
	s = NewServer(serverSettings(t, map[string]any{"http.host": "::1", "http.port": "4000"}), nethttp.NotFoundHandler(), NewGuard(nil, true), noRoutes, &tests.Log{})
	s.listen = func(addr string) (net.Listener, error) { got = addr; return nil, errors.New("stop") }
	_ = s.Run()
	if got != "[::1]:4000" {
		t.Fatalf("an IPv6 host must be bracketed: %q", got)
	}
}

func TestShutdownLetsARunningRequestFinishWithinTheGrace(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path != "/slow" {
			return
		}
		close(started)
		<-release
		_, _ = io.WriteString(w, "finished")
	}), map[string]any{"http.server.shutdown_grace": "5s"})
	done := runInBackground(s)
	waitFor(t, "the server", func() bool {
		c, err := net.Dial("tcp", base[len("http://"):])
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})

	got := make(chan string, 1)
	go func() {
		resp, err := nethttp.Get(base + "/slow")
		if err != nil {
			got <- err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		got <- string(b)
	}()
	<-started
	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown() }()
	select {
	case <-stopped:
		t.Fatal("Shutdown returned while a request was still running")
	case <-time.After(150 * time.Millisecond):
	}
	if resp, err := nethttp.Get(base + "/other"); err == nil {
		resp.Body.Close()
		t.Fatal("a new connection was accepted during shutdown")
	}
	close(release)
	if body := <-got; body != "finished" {
		t.Fatalf("the running request was cut: %q", body)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCutsARequestThatOutlastsTheGraceAndWaitsForItsHandler(t *testing.T) {
	started := make(chan struct{})
	var handlerReturned atomic.Bool
	s, l, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		close(started)
		<-r.Context().Done()
		time.Sleep(100 * time.Millisecond)
		handlerReturned.Store(true)
	}), map[string]any{"http.server.shutdown_grace": "1s"})
	s.grace = 150 * time.Millisecond
	done := runInBackground(s)
	waitFor(t, "the server", func() bool {
		c, err := net.Dial("tcp", base[len("http://"):])
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	go func() {
		if resp, err := nethttp.Get(base + "/stuck"); err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	begin := time.Now()
	if err := s.Shutdown(); err != nil {
		t.Fatalf("running out of grace is expected, not an error: %v", err)
	}
	if took := time.Since(begin); took > 3*time.Second {
		t.Fatalf("Shutdown took %v", took)
	}
	if !handlerReturned.Load() {
		t.Fatal("Shutdown returned while a handler was still running: the stores would be closed under it")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(l.Lines(), "\n"), "grace") {
		t.Fatalf("the cut was not logged: %q", l.Lines())
	}
}

func TestShutdownGivesUpWaitingForAHandlerThatNeverReturns(t *testing.T) {
	started, never := make(chan struct{}), make(chan struct{})
	defer close(never)
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		close(started)
		<-never
	}), nil)
	s.grace, s.handlerWait = 100*time.Millisecond, 200*time.Millisecond
	done := runInBackground(s)
	waitFor(t, "the server", func() bool {
		c, err := net.Dial("tcp", base[len("http://"):])
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	go func() {
		if resp, err := nethttp.Get(base + "/hang"); err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	begin := time.Now()
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(begin); took > 2*time.Second {
		t.Fatalf("Shutdown waited %v for a handler that never returns", took)
	}
	<-done
}

func TestShutdownBeforeRunPreventsTheStart(t *testing.T) {
	s, _, _ := newServer(t, nethttp.NotFoundHandler(), nil)
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(); err != nil {
		t.Fatalf("a server stopped before it started must not serve or fail: %v", err)
	}
}

func TestASecondSignalDuringShutdownExitsWithCodeOne(t *testing.T) {
	started, never := make(chan struct{}), make(chan struct{})
	defer close(never)
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		close(started)
		<-never
	}), map[string]any{"http.server.shutdown_grace": "10m"})
	sig := make(chan os.Signal, 1)
	exited := make(chan int, 1)
	s.signals = func() (<-chan os.Signal, func()) { return sig, func() {} }
	s.exit = func(code int) { exited <- code }
	done := runInBackground(s)
	waitFor(t, "the server", func() bool {
		c, err := net.Dial("tcp", base[len("http://"):])
		if err == nil {
			_ = c.Close()
		}
		return err == nil
	})
	go func() {
		if resp, err := nethttp.Get(base + "/hang"); err == nil {
			resp.Body.Close()
		}
	}()
	<-started
	go func() { _ = s.Shutdown() }()
	select {
	case code := <-exited:
		t.Fatalf("exited with %d before a second signal", code)
	case <-time.After(100 * time.Millisecond):
	}
	sig <- os.Interrupt
	select {
	case code := <-exited:
		if code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a second signal did not end the process")
	}
	_ = done
}

func TestTheSignalWatchIsReleasedWhenShutdownEnds(t *testing.T) {
	s, _, _ := newServer(t, nethttp.NotFoundHandler(), nil)
	var stopped atomic.Bool
	s.signals = func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() { stopped.Store(true) } }
	s.exit = func(int) { t.Error("exit called without a second signal") }
	done := runInBackground(s)
	waitFor(t, "the server", func() bool { return s.running() })
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	<-done
	if !stopped.Load() {
		t.Fatal("the signal subscription leaked")
	}
}

func TestInFlightCountsOnlyRunningRequests(t *testing.T) {
	var s *Server
	seen := int64(-1)
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { seen = s.inflight.Load() }), nil)
	done := runInBackground(s)
	waitFor(t, "the server", func() bool {
		resp, err := nethttp.Get(base)
		if err == nil {
			resp.Body.Close()
		}
		return err == nil
	})
	waitFor(t, "the counter to drop", func() bool { return s.inflight.Load() == 0 })
	if seen != 1 {
		t.Fatalf("a handler saw %d requests in flight, want 1", seen)
	}
	_ = s.Shutdown()
	<-done
}

func TestTLSIsServedWhenBothFilesAreSet(t *testing.T) {
	cert, key := writeSelfSigned(t)
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { _, _ = io.WriteString(w, "secure") }),
		map[string]any{"http.tls.ssl.cert": cert, "http.tls.ssl.key": key})
	done := runInBackground(s)
	client := &nethttp.Client{Transport: &nethttp.Transport{TLSClientConfig: testTLS(t, cert)}}
	var body string
	waitFor(t, "the TLS server", func() bool {
		resp, err := client.Get("https://" + base[len("http://"):])
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body = string(b)
		return true
	})
	if body != "secure" {
		t.Fatalf("body %q", body)
	}
	_ = s.Shutdown()
	<-done
}

func TestTheServerHandlerIsTheRewriteInFrontOfTheRouter(t *testing.T) {
	a := newApp(t, nil, true)
	a.guard.Handle(a.route, ClassPublic, "GET", "/api/server-pub", reached)
	s := NewServer(serverSettings(t, nil), Rewrite(a.route), a.guard, noRoutes, &tests.Log{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodGet, "/api//server-pub", nil)
	s.handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("a non-canonical path reached the router: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(nethttp.MethodGet, "/api/server-pub", nil)
	req.RemoteAddr, req.Host = "127.0.0.1:5000", "localhost:20128"
	s.handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

var _ = context.Background

type fakeGate struct {
	opened chan struct{}
	ok     bool
}

func newFakeGate() *fakeGate { return &fakeGate{opened: make(chan struct{})} }

func (g *fakeGate) Wait(ctx context.Context) bool {
	select {
	case <-g.opened:
		return g.ok
	case <-ctx.Done():
		return false
	}
}

func (g *fakeGate) decide(ok bool) { g.ok = ok; close(g.opened) }

func dialable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
	if err == nil {
		_ = c.Close()
	}
	return err == nil
}

func TestTheSocketOpensBeforeTheGateAndNothingIsServedUntilItOpens(t *testing.T) {
	served := make(chan struct{}, 4)
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		served <- struct{}{}
		_, _ = io.WriteString(w, "ok")
	}), nil)
	gate := newFakeGate()
	s.WaitFor(gate)
	done := runInBackground(s)
	addr := base[len("http://"):]
	waitFor(t, "the socket", func() bool { return dialable(addr) })

	client := &nethttp.Client{Timeout: 250 * time.Millisecond}
	if resp, err := client.Get(base + "/x"); err == nil {
		resp.Body.Close()
		t.Fatal("a request was answered before the data was ready")
	}
	select {
	case <-served:
		t.Fatal("the handler ran before the data was ready")
	default:
	}

	gate.decide(true)
	waitFor(t, "the first answer", func() bool {
		resp, err := nethttp.Get(base + "/x")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a normal stop is not an error: %v", err)
	}
}

func TestAPortInUseIsStillTheFirstErrorWithAGate(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	host, port, _ := net.SplitHostPort(taken.Addr().String())
	s := NewServer(serverSettings(t, map[string]any{"http.host": host, "http.port": port}), nethttp.NotFoundHandler(), NewGuard(nil, true), noRoutes, &tests.Log{})
	s.WaitFor(newFakeGate())
	err = s.Run()
	if err == nil || !strings.Contains(err.Error(), taken.Addr().String()) {
		t.Fatalf("error = %v", err)
	}
}

func TestAFailedGateStopsTheServerWithoutServingAnything(t *testing.T) {
	s, l, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		t.Error("a request reached the handler although the data failed")
	}), nil)
	gate := newFakeGate()
	s.WaitFor(gate)
	done := runInBackground(s)
	waitFor(t, "the socket", func() bool { return dialable(base[len("http://"):]) })
	gate.decide(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the data runner reports the cause; the server just stops: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the server kept waiting for data that will never come")
	}
	if dialable(base[len("http://"):]) {
		t.Fatal("the socket stayed open after the data failed")
	}
	_ = l
}

func TestShutdownWhileWaitingForTheGateEndsTheRunWithoutServing(t *testing.T) {
	s, _, base := newServer(t, nethttp.NotFoundHandler(), nil)
	s.WaitFor(newFakeGate())
	done := runInBackground(s)
	waitFor(t, "the socket", func() bool { return dialable(base[len("http://"):]) })
	if err := s.Shutdown(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run kept waiting after Shutdown")
	}
	if dialable(base[len("http://"):]) {
		t.Fatal("the socket stayed open")
	}
}

func TestWithoutAGateTheServerServesAtOnce(t *testing.T) {
	s, _, base := newServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { _, _ = io.WriteString(w, "now") }), nil)
	done := runInBackground(s)
	waitFor(t, "an answer", func() bool {
		resp, err := nethttp.Get(base + "/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
	_ = s.Shutdown()
	<-done
}
