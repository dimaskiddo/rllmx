package oauth

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/network"
)

const (
	listenerGrace  = 5 * time.Second
	listenerTick   = 15 * time.Second
	listenerHeader = 10 * time.Second
	listenerHandle = 30 * time.Second
)

const donePage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Router-LLM</title></head><body><p>You can close this tab and return to the dashboard.</p></body></html>`

// Listeners serves the fixed callback ports. One listener exists per port and
// is shared by every login waiting on it; it stops when none is left. It
// answers only the callback path, only for a loopback Host, and only for a
// state that a pending login holds on that port.
type Listeners struct {
	// Host is the bind address.
	Host     string
	Sessions *Sessions
	// Handle finishes a login that the browser brought to a port.
	Handle func(ctx context.Context, port int, state string, in Input) error
	Log    log.Log
	Now    func() time.Time

	mu     sync.Mutex
	ports  map[int]*listener
	closed bool
}

type listener struct {
	srv  *http.Server
	last time.Time
	done chan struct{}
}

// Acquire makes sure a listener serves port and path. It returns false when
// the port cannot be bound, and the login then falls back to pasting.
func (l *Listeners) Acquire(port int, path string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	if l.ports == nil {
		l.ports = map[int]*listener{}
	}
	now := l.now()
	if cur, ok := l.ports[port]; ok {
		cur.last = now
		return true
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(l.Host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	cur := &listener{last: now, done: make(chan struct{})}
	cur.srv = &http.Server{
		Handler:           l.handler(port, path),
		ReadHeaderTimeout: listenerHeader,
		MaxHeaderBytes:    16 << 10,
	}
	l.ports[port] = cur
	go func() {
		if err := cur.srv.Serve(ln); err != nil && err != http.ErrServerClosed && l.Log != nil {
			l.Log.Warningf("oauth: the callback listener on port %d stopped: %v", port, err)
		}
	}()
	go l.watch(port, cur)
	return true
}

func (l *Listeners) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// watch stops a listener once no login waits on it, which also ends one whose
// logins expired without a callback.
func (l *Listeners) watch(port int, cur *listener) {
	t := time.NewTicker(listenerTick)
	defer t.Stop()
	for {
		select {
		case <-cur.done:
			return
		case <-t.C:
			l.Release(port)
		}
	}
}

// Release stops the listener on port when no login waits on it.
func (l *Listeners) Release(port int) {
	l.mu.Lock()
	cur, ok := l.ports[port]
	if !ok || l.now().Sub(cur.last) < listenerGrace || l.Sessions.Waiting(port) > 0 {
		l.mu.Unlock()
		return
	}
	delete(l.ports, port)
	l.mu.Unlock()
	// The caller may be this listener's own handler, which Shutdown waits for.
	go cur.stop()
}

func (c *listener) stop() {
	close(c.done)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

// Running reports whether a listener serves port.
func (l *Listeners) Running(port int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.ports[port]
	return ok
}

// Close stops every listener; none starts afterwards.
func (l *Listeners) Close() {
	l.mu.Lock()
	l.closed = true
	all := l.ports
	l.ports = nil
	l.mu.Unlock()
	for _, cur := range all {
		cur.stop()
	}
}

func (l *Listeners) handler(port int, path string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if r.Method != http.MethodGet || r.URL.Path != path || !network.IsLoopbackHost(host) {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		state := q.Get("state")
		if p, ok := l.Sessions.Peek(state); !ok || p != port {
			http.NotFound(w, r)
			return
		}
		in := Input{Code: q.Get("code"), State: state, Failed: q.Get("error") != ""}
		ctx, cancel := context.WithTimeout(r.Context(), listenerHandle)
		defer cancel()
		_ = l.Handle(ctx, port, state, in)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(donePage))
	})
}
