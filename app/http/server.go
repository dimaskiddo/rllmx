package http

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	nethttp "net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/platform/settings"
)

const (
	serverSignature = "rllmx:http"
	// shutdownPriority puts the server ahead of every runner that holds a
	// resource its handlers use: those keep the framework default of 0.
	shutdownPriority = -100
	handlerWait      = 5 * time.Second
)

// Ready tells the server when the data is ready. Wait blocks until it is decided
// or ctx ends, and reports whether the data opened.
type Ready interface {
	Wait(ctx context.Context) bool
}

// Server is the runner that owns the http.Server. The driver builds its own
// server without header or idle timeouts and with no way to set them, so the
// stock goravel:http runner is disabled and this one serves the router.
type Server struct {
	settings *settings.Server
	next     nethttp.Handler
	guard    *Guard
	routes   func() []contractshttp.Info
	log      log.Log
	ready    Ready

	grace       time.Duration
	handlerWait time.Duration
	listen      func(addr string) (net.Listener, error)
	signals     func() (<-chan os.Signal, func())
	exit        func(code int)

	inflight atomic.Int64
	mu       sync.Mutex
	srv      *nethttp.Server
	stopped  bool
	// stop is closed by Shutdown, so a wait for the data ends at once.
	stop chan struct{}
}

// NewServer builds the runner. handler is what serves requests, normally
// Rewrite(Route()). routes lists the registered routes so the start can refuse
// one that is not in the guard's table.
func NewServer(cfg *settings.Server, handler nethttp.Handler, guard *Guard, routes func() []contractshttp.Info, l log.Log) *Server {
	return &Server{
		settings: cfg, next: handler, guard: guard, routes: routes, log: l,
		grace: cfg.ShutdownGrace, handlerWait: handlerWait,
		listen:  func(addr string) (net.Listener, error) { return net.Listen("tcp", addr) },
		signals: notifySignals,
		exit:    os.Exit,
		stop:    make(chan struct{}),
	}
}

func notifySignals() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	return ch, func() { signal.Stop(ch) }
}

// WaitFor makes Run hold every request until r says the data is ready. The
// socket still opens first, so a port in use stays the first error. Without it
// the server serves at once.
func (s *Server) WaitFor(r Ready) { s.ready = r }

// Signature is the runner's name in the application.
func (s *Server) Signature() string { return serverSignature }

// ShouldRun is always true: the server is what the process is for.
func (s *Server) ShouldRun() bool { return true }

// ShutdownPriority orders the stop: groups go in ascending order.
func (s *Server) ShutdownPriority() int { return shutdownPriority }

// handler counts the requests in flight, so a stop can wait for them, and puts
// the compressed writer below everything the router builds, so both a handler's
// answer and a stream pass through it.
func (s *Server) handler() nethttp.Handler {
	next := Compress(s.settings.Compression, s.next)
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s.inflight.Add(1)
		defer s.inflight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

func (s *Server) build() *nethttp.Server {
	protocols := new(nethttp.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	// The plain listener speaks HTTP/2 only when the operator asks: a proxy that
	// multiplexes streams wants it, and anything else is a client that meant
	// HTTP/1.
	protocols.SetUnencryptedHTTP2(s.settings.H2C)
	return &nethttp.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: s.settings.ReadHeaderTimeout,
		IdleTimeout:       s.settings.IdleTimeout,
		MaxHeaderBytes:    s.settings.HeaderLimitKiB << 10,
		Protocols:         protocols,
		// A stated floor instead of Go's own: TLS 1.2 is the oldest this router
		// answers on, in case a future default moves.
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func (s *Server) addr() string {
	return net.JoinHostPort(s.settings.Host, strconv.Itoa(s.settings.Port))
}

func (s *Server) running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv != nil
}

// Run checks the route table, opens the socket and serves until Shutdown. The
// socket opens before anything else could fail, so a port in use is the first
// error. A normal stop returns nil.
func (s *Server) Run() error {
	if missing := s.guard.Unclassified(s.routes()); len(missing) > 0 {
		return fmt.Errorf("http: routes outside the guard table: %s", strings.Join(missing, ", "))
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	addr := s.addr()
	ln, err := s.listen(addr)
	if err != nil {
		return fmt.Errorf("http: listen on %s: %w", addr, err)
	}
	srv := s.build()
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		_ = ln.Close()
		return nil
	}
	s.srv = srv
	s.mu.Unlock()

	s.log.Infof("listening on %s", ln.Addr())
	if s.ready != nil && !s.awaitData(ln) {
		return nil
	}
	if s.settings.CertFile != "" {
		err = srv.ServeTLS(ln, s.settings.CertFile, s.settings.KeyFile)
	} else {
		err = srv.Serve(ln)
	}
	if errors.Is(err, nethttp.ErrServerClosed) {
		return nil
	}
	return err
}

// awaitData holds the open socket until the data is ready. Connections queue
// in the kernel meanwhile and nothing is read from them. It returns false, with
// the socket closed, when the data failed or the server was stopped first; the
// data runner reports the cause, so the server just ends.
func (s *Server) awaitData(ln net.Listener) bool {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	if s.ready.Wait(ctx) && !s.isStopped() {
		return true
	}
	_ = ln.Close()
	s.mu.Lock()
	s.srv = nil
	s.mu.Unlock()
	return false
}

func (s *Server) isStopped() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped
}

// Shutdown stops accepting, lets running requests finish for the grace period,
// then cuts what is left and waits a few seconds for the handlers to return, so
// the runners that stop after this one never close a store under a handler. A
// second interrupt or terminate signal while it works exits with code 1.
func (s *Server) Shutdown() error {
	sig, release := s.signals()
	defer release()
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		select {
		case <-sig:
			s.exit(1)
		case <-quit:
		}
	}()

	s.mu.Lock()
	if !s.stopped {
		close(s.stop)
	}
	s.stopped = true
	srv := s.srv
	s.mu.Unlock()
	if srv == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.grace)
	defer cancel()
	err := srv.Shutdown(ctx)
	if err == nil {
		return nil
	}
	_ = srv.Close()
	s.waitForHandlers()
	if errors.Is(err, context.DeadlineExceeded) {
		s.log.Warningf("shutdown: requests still running after the %s grace were cut", s.grace)
		return nil
	}
	return fmt.Errorf("http: shutdown: %w", err)
}

func (s *Server) waitForHandlers() {
	deadline := time.Now().Add(s.handlerWait)
	for s.inflight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}
