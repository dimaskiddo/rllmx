package tests

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// Call is one request the fake upstream received. Body and Header are copies.
type Call struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// Reply is one scripted answer. A zero Status is 200.
type Reply struct {
	Status      int
	ContentType string
	Body        string
	// Chunks, when set, are written as separate flushed pieces with Pause
	// between them, instead of Body.
	Chunks []string
	Pause  time.Duration
	// Hang holds the connection open until the test ends, before any header.
	Hang bool
	// Silent sends the status and headers, then holds the body open and quiet.
	Silent bool
	// Header adds response headers.
	Header map[string]string
}

// Upstream is an httptest server that answers from a script, one Reply per
// request, and keeps the last Reply for any request after the script ends.
type Upstream struct {
	*httptest.Server

	mu    sync.Mutex
	calls []Call
	next  int
	steps []Reply
	done  chan struct{}
}

// NewUpstream starts a fake upstream that serves steps in order.
func NewUpstream(t *testing.T, steps ...Reply) *Upstream {
	t.Helper()
	u := &Upstream{steps: steps, done: make(chan struct{})}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(func() {
		close(u.done)
		u.Server.Close()
	})
	return u
}

// Calls returns the requests received so far.
func (u *Upstream) Calls() []Call {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]Call(nil), u.calls...)
}

// Reset replaces the script and forgets earlier calls.
func (u *Upstream) Reset(steps ...Reply) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.steps, u.next, u.calls = steps, 0, nil
}

func (u *Upstream) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	u.mu.Lock()
	u.calls = append(u.calls, Call{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
	var rep Reply
	if len(u.steps) > 0 {
		i := min(u.next, len(u.steps)-1)
		rep = u.steps[i]
		u.next++
	}
	u.mu.Unlock()

	if rep.Hang {
		select {
		case <-u.done:
		case <-r.Context().Done():
		}
		return
	}
	if rep.ContentType != "" {
		w.Header().Set("Content-Type", rep.ContentType)
	}
	for k, v := range rep.Header {
		w.Header().Set(k, v)
	}
	status := rep.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if rep.Silent {
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-u.done:
		case <-r.Context().Done():
		}
		return
	}
	if len(rep.Chunks) == 0 {
		_, _ = io.WriteString(w, rep.Body)
		return
	}
	f, _ := w.(http.Flusher)
	for _, c := range rep.Chunks {
		if _, err := io.WriteString(w, c); err != nil {
			return
		}
		if f != nil {
			f.Flush()
		}
		if rep.Pause > 0 {
			select {
			case <-time.After(rep.Pause):
			case <-r.Context().Done():
				return
			}
		}
	}
}
