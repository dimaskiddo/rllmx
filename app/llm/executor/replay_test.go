package executor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"rllmx/tests/llm"
)

// failThenSucceed serves one stale-socket failure, then a 200.
type failThenSucceed struct {
	calls atomic.Int32
	err   error
}

func (f *failThenSucceed) RoundTrip(*nethttp.Request) (*nethttp.Response, error) {
	if f.calls.Add(1) == 1 {
		return nil, &url.Error{Op: "Post", URL: "https://x.example", Err: f.err}
	}
	return &nethttp.Response{StatusCode: 200, Header: nethttp.Header{}, Body: io.NopCloser(&empty{})}, nil
}

type empty struct{}

func (*empty) Read([]byte) (int, error) { return 0, io.EOF }

func TestADeadPooledSocketIsReplayedOnceWithoutAWait(t *testing.T) {
	rt := &failThenSucceed{err: syscall.ECONNRESET}
	var waits []time.Duration
	d := &Default{Client: &nethttp.Client{Transport: rt}, Sleep: instant(&waits)}
	resp, err := d.Execute(context.Background(), Request{URL: "https://x.example/v1", Key: llm.Secret, Auth: llm.Bearer()})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	_ = resp.Body.Close()
	if resp.Status != 200 || rt.calls.Load() != 2 || len(waits) != 0 {
		t.Fatalf("status %d after %d calls and %d waits, want 200 after 2 with none", resp.Status, rt.calls.Load(), len(waits))
	}
}

// alwaysFails fails every attempt with one error.
type alwaysFails struct {
	calls atomic.Int32
	err   error
}

func (f *alwaysFails) RoundTrip(*nethttp.Request) (*nethttp.Response, error) {
	f.calls.Add(1)
	return nil, &url.Error{Op: "Post", URL: "https://x.example", Err: f.err}
}

func TestOnlyAStaleSocketIsReplayed(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		calls int32
	}{
		"a refusal is not stale":     {syscall.ECONNREFUSED, 4},
		"a DNS failure is not stale": {&net.DNSError{Err: "no such host", IsNotFound: true}, 4},
		"a timeout is not stale":     {context.DeadlineExceeded, 4},
		"a certificate is not stale": {&tls.CertificateVerificationError{Err: &x509.UnknownAuthorityError{}}, 4},
		"a reset is stale":           {syscall.ECONNRESET, 5},
		"a broken pipe is stale":     {syscall.EPIPE, 5},
	} {
		t.Run(name, func(t *testing.T) {
			rt := &alwaysFails{err: tc.err}
			var waits []time.Duration
			d := &Default{Client: &nethttp.Client{Transport: rt}, Sleep: instant(&waits)}
			if _, err := d.Execute(context.Background(), Request{URL: "https://x.example/v1", Key: llm.Secret, Auth: llm.Bearer()}); err != ErrUnreachable {
				t.Fatalf("err = %v, want ErrUnreachable", err)
			}
			if got := rt.calls.Load(); got != tc.calls {
				t.Fatalf("%d calls, want %d", got, tc.calls)
			}
			if tc.calls == 5 && len(waits) != 3 {
				t.Fatalf("the replay's waits = %v, want the table's three after it", waits)
			}
		})
	}
}

func TestACancelledRequestSendsNothing(t *testing.T) {
	rt := &alwaysFails{err: syscall.ECONNRESET}
	d := &Default{Client: &nethttp.Client{Transport: rt}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Execute(ctx, Request{URL: "https://x.example/v1", Key: llm.Secret, Auth: llm.Bearer()}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
	if got := rt.calls.Load(); got > 1 {
		t.Fatalf("%d calls after cancellation, want at most the one already in flight", got)
	}
}

func TestAKilledIdleConnectionIsReplayedAgainstTheRealServer(t *testing.T) {
	// The server answers once and then closes the connection, as an edge that
	// drops idle sockets does; the second call must dial again and succeed.
	var dials atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
		w.(nethttp.Flusher).Flush()
		if c, ok := w.(nethttp.Hijacker); ok {
			if conn, _, err := c.Hijack(); err == nil {
				_ = conn.Close()
			}
		}
	}))
	srv.Listener.Close()
	srv.Listener = &countedListener{Listener: ln, dials: &dials}
	srv.Start()
	defer srv.Close()
	d := &Default{Client: srv.Client()}
	req := Request{URL: srv.URL + "/v1", Key: llm.Secret, Auth: llm.Bearer()}
	for i := 0; i < 2; i++ {
		resp, err := d.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if got := dials.Load(); got < 2 {
		t.Fatalf("the second call reused a dead connection: %d dials", got)
	}
}

// countedListener counts accepted connections.
type countedListener struct {
	net.Listener
	dials *atomic.Int32
}

func (c *countedListener) Accept() (net.Conn, error) {
	conn, err := c.Listener.Accept()
	if err == nil {
		c.dials.Add(1)
	}
	return conn, err
}

// instant is a sleep that records and returns at once.
func instant(waits *[]time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		*waits = append(*waits, d)
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
}

var _ = errors.Is

func TestTheStaleSocketClassification(t *testing.T) {
	cases := map[string]struct {
		err   error
		stale bool
	}{
		"a reset":             {syscall.ECONNRESET, true},
		"a broken pipe":       {syscall.EPIPE, true},
		"a closed socket":     {net.ErrClosed, true},
		"an early end":        {io.ErrUnexpectedEOF, true},
		"a refusal":           {syscall.ECONNREFUSED, false},
		"an unreachable host": {syscall.EHOSTUNREACH, false},
		"a DNS failure":       {&net.DNSError{Err: "no such host", IsNotFound: true}, false},
		"a cancellation":      {context.Canceled, false},
		"a deadline":          {context.DeadlineExceeded, false},
		"a certificate":       {&tls.CertificateVerificationError{Err: &x509.UnknownAuthorityError{}}, false},
		"nothing":             {nil, false},
	}
	for name, c := range cases {
		var wrapped error
		if c.err != nil {
			wrapped = &url.Error{Op: "Post", URL: "https://x.example", Err: c.err}
		}
		if got := isStaleSocketError(wrapped); got != c.stale {
			t.Errorf("%s: %v, want %v", name, got, c.stale)
		}
	}
}
