package network

import (
	"bytes"
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"strings"
	"testing"
	"time"
)

type fakeRoundTripper struct {
	calls  int
	reply  func(*nethttp.Request) (*nethttp.Response, error)
	closed int
}

func (f *fakeRoundTripper) RoundTrip(req *nethttp.Request) (*nethttp.Response, error) {
	f.calls++
	if f.reply != nil {
		return f.reply(req)
	}
	return &nethttp.Response{StatusCode: nethttp.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

func (f *fakeRoundTripper) CloseIdleConnections() { f.closed++ }

func fakeReply(status int) func(*nethttp.Request) (*nethttp.Response, error) {
	return func(*nethttp.Request) (*nethttp.Response, error) {
		return &nethttp.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}
}

func failingReply(err error) func(*nethttp.Request) (*nethttp.Response, error) {
	return func(*nethttp.Request) (*nethttp.Response, error) { return nil, err }
}

func newTestHybrid(h2, h3 *fakeRoundTripper) *hybridTransport {
	return &hybridTransport{h2: h2, h3: h3, cache: &http3Cache{dead: map[string]time.Time{}}}
}

func TestTheHybridSendsOverHTTP3First(t *testing.T) {
	h2, h3 := &fakeRoundTripper{}, &fakeRoundTripper{}
	req, _ := nethttp.NewRequest("GET", "https://api.example/v1", nil)
	if _, err := newTestHybrid(h2, h3).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if h3.calls != 1 || h2.calls != 0 {
		t.Fatalf("h3 %d h2 %d: the HTTP/3 attempt must win", h3.calls, h2.calls)
	}
}

func TestTheHybridFallsBackToHTTP2AndKeepsTheBodyWhole(t *testing.T) {
	payload := bytes.Repeat([]byte("a"), 4096)
	h2 := &fakeRoundTripper{reply: func(req *nethttp.Request) (*nethttp.Response, error) {
		got, _ := io.ReadAll(req.Body)
		if !bytes.Equal(got, payload) {
			t.Fatalf("h2 saw %d bytes, want %d", len(got), len(payload))
		}
		return &nethttp.Response{StatusCode: nethttp.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}}
	h3 := &fakeRoundTripper{reply: func(req *nethttp.Request) (*nethttp.Response, error) {
		_, _ = io.Copy(io.Discard, req.Body)
		return nil, errors.New("no quic")
	}}
	req, _ := nethttp.NewRequest("POST", "https://api.example/v1", bytes.NewReader(payload))
	if _, err := newTestHybrid(h2, h3).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if h3.calls != 1 || h2.calls != 1 {
		t.Fatalf("h3 %d h2 %d", h3.calls, h2.calls)
	}
}

func TestTheHybridSkipsHTTP3ForPlainHTTP(t *testing.T) {
	h2, h3 := &fakeRoundTripper{}, &fakeRoundTripper{}
	req, _ := nethttp.NewRequest("GET", "http://localhost:11434/api/chat", nil)
	if _, err := newTestHybrid(h2, h3).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if h3.calls != 0 || h2.calls != 1 {
		t.Fatalf("h3 %d h2 %d: plain HTTP must not attempt QUIC", h3.calls, h2.calls)
	}
}

func TestTheHybridSkipsHTTP3WhenMarked(t *testing.T) {
	h2, h3 := &fakeRoundTripper{}, &fakeRoundTripper{}
	req, _ := nethttp.NewRequestWithContext(WithoutHTTP3(context.Background()), "GET", "https://api.example/v1", nil)
	if _, err := newTestHybrid(h2, h3).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if h3.calls != 0 || h2.calls != 1 {
		t.Fatalf("h3 %d h2 %d", h3.calls, h2.calls)
	}
}

func TestTheHybridRemembersAFailedHost(t *testing.T) {
	h2 := &fakeRoundTripper{reply: fakeReply(nethttp.StatusOK)}
	h3 := &fakeRoundTripper{reply: failingReply(errors.New("no quic"))}
	h := newTestHybrid(h2, h3)
	for range 2 {
		req, _ := nethttp.NewRequest("GET", "https://api.example/v1", nil)
		if _, err := h.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
	if h3.calls != 1 || h2.calls != 2 {
		t.Fatalf("h3 %d h2 %d: the second call must skip the failed host", h3.calls, h2.calls)
	}
}

func TestTheHybridDoesNotRetryACancelledRequest(t *testing.T) {
	h2 := &fakeRoundTripper{}
	h3 := &fakeRoundTripper{reply: failingReply(context.Canceled)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := nethttp.NewRequestWithContext(ctx, "GET", "https://api.example/v1", nil)
	if _, err := newTestHybrid(h2, h3).RoundTrip(req); err == nil {
		t.Fatal("a cancelled request must not be retried")
	}
	if h2.calls != 0 {
		t.Fatalf("h2 called %d times after cancellation", h2.calls)
	}
}

func TestTheHybridClosesBothTransports(t *testing.T) {
	h2, h3 := &fakeRoundTripper{}, &fakeRoundTripper{}
	newTestHybrid(h2, h3).CloseIdleConnections()
	if h2.closed != 1 || h3.closed != 1 {
		t.Fatalf("h2 closed %d h3 closed %d", h2.closed, h3.closed)
	}
}

func TestTheHybridAppliesTheHeaderTimeoutToHTTP2Only(t *testing.T) {
	base := &nethttp.Transport{}
	h := &hybridTransport{h2: base, h3: &fakeRoundTripper{}, cache: &http3Cache{dead: map[string]time.Time{}}}
	got, ok := h.WithResponseHeaderTimeout(3 * time.Second).(*hybridTransport)
	if !ok {
		t.Fatal("a copy that is not a hybrid")
	}
	if got.h2.(*nethttp.Transport).ResponseHeaderTimeout != 3*time.Second {
		t.Fatal("the copy did not take the provider's bound")
	}
	if base.ResponseHeaderTimeout != 0 {
		t.Fatal("the shared transport was mutated")
	}
}
