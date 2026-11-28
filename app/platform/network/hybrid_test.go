package network

import (
	"bytes"
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"net/http/httptrace"
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

// newTestHybrid returns a hybrid whose host api.example already advertised
// HTTP/3, so a test of the QUIC path starts where a warm client would.
func newTestHybrid(h2, h3 *fakeRoundTripper) *hybridTransport {
	h := newColdHybrid(h2, h3)
	h.cache.learn("api.example", time.Now().Add(time.Hour))
	return h
}

func newColdHybrid(h2, h3 *fakeRoundTripper) *hybridTransport {
	return &hybridTransport{h2: h2, h3: h3, cache: newHTTP3Cache()}
}

func advertising(value string) func(*nethttp.Request) (*nethttp.Response, error) {
	return func(*nethttp.Request) (*nethttp.Response, error) {
		h := nethttp.Header{}
		h.Set("Alt-Svc", value)
		return &nethttp.Response{StatusCode: nethttp.StatusOK, Header: h, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}
}

func get(t *testing.T, h nethttp.RoundTripper, url string) {
	t.Helper()
	req, _ := nethttp.NewRequest("GET", url, nil)
	resp, err := h.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
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
	h := &hybridTransport{h2: base, h3: &fakeRoundTripper{}, cache: newHTTP3Cache()}
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

// wroteThenFailed is an HTTP/3 attempt that sent the whole request and then
// lost the stream, the way a reset after the upstream took the call looks.
func wroteThenFailed(err error) func(*nethttp.Request) (*nethttp.Response, error) {
	return func(req *nethttp.Request) (*nethttp.Response, error) {
		if tr := httptrace.ContextClientTrace(req.Context()); tr != nil && tr.WroteRequest != nil {
			tr.WroteRequest(httptrace.WroteRequestInfo{})
		}
		return nil, err
	}
}

func TestTheHybridDoesNotResendARequestThatWasWritten(t *testing.T) {
	h2 := &fakeRoundTripper{}
	h3 := &fakeRoundTripper{reply: wroteThenFailed(errors.New("stream reset"))}
	h := newTestHybrid(h2, h3)
	req, _ := nethttp.NewRequest("POST", "https://api.example/v1", strings.NewReader("{}"))
	if _, err := h.RoundTrip(req); err == nil {
		t.Fatal("a request the upstream may have taken was answered by a second send")
	}
	if h2.calls != 0 {
		t.Fatalf("h2 sent %d times after the request was written over HTTP/3", h2.calls)
	}
	if h.cache.usable("api.example") {
		t.Fatal("a host whose QUIC stream broke was not remembered")
	}
}

func TestTheHybridResendsARequestThatWasNeverWritten(t *testing.T) {
	h2 := &fakeRoundTripper{}
	h3 := &fakeRoundTripper{reply: failingReply(errors.New("handshake timeout"))}
	req, _ := nethttp.NewRequest("POST", "https://api.example/v1", strings.NewReader("{}"))
	if _, err := newTestHybrid(h2, h3).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if h2.calls != 1 {
		t.Fatalf("h2 sent %d times, want 1", h2.calls)
	}
}

func TestTheHybridBoundsTheWaitForHTTP3Headers(t *testing.T) {
	h2 := &fakeRoundTripper{}
	h3 := &fakeRoundTripper{reply: func(req *nethttp.Request) (*nethttp.Response, error) {
		if tr := httptrace.ContextClientTrace(req.Context()); tr != nil && tr.WroteRequest != nil {
			tr.WroteRequest(httptrace.WroteRequestInfo{})
		}
		<-req.Context().Done()
		return nil, req.Context().Err()
	}}
	h := newTestHybrid(h2, h3)
	h.headerBound = 30 * time.Millisecond
	req, _ := nethttp.NewRequest("POST", "https://api.example/v1", strings.NewReader("{}"))
	start := time.Now()
	_, err := h.RoundTrip(req)
	var te interface{ Timeout() bool }
	if !errors.As(err, &te) || !te.Timeout() {
		t.Fatalf("err %v, want a timeout", err)
	}
	if time.Since(start) > 2*time.Second || h2.calls != 0 {
		t.Fatalf("took %v, h2 %d", time.Since(start), h2.calls)
	}
}

func TestTheHeaderBoundDoesNotCutTheBodyAfterHeaders(t *testing.T) {
	var ctx context.Context
	h3 := &fakeRoundTripper{reply: func(req *nethttp.Request) (*nethttp.Response, error) {
		ctx = req.Context()
		return &nethttp.Response{StatusCode: nethttp.StatusOK, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	}}
	h := newTestHybrid(&fakeRoundTripper{}, h3)
	h.headerBound = 20 * time.Millisecond
	req, _ := nethttp.NewRequest("POST", "https://api.example/v1", strings.NewReader("{}"))
	resp, err := h.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	if ctx.Err() != nil {
		t.Fatal("the bound cut an answer whose headers had arrived")
	}
	_ = resp.Body.Close()
	if ctx.Err() == nil {
		t.Fatal("closing the body left its context running")
	}
}

func TestTheHybridUsesHTTP2UntilTheHostAdvertisesHTTP3(t *testing.T) {
	h2 := &fakeRoundTripper{reply: advertising(`h3=":443"; ma=3600`)}
	h3 := &fakeRoundTripper{}
	h := newColdHybrid(h2, h3)
	get(t, h, "https://api.example/v1")
	if h2.calls != 1 || h3.calls != 0 {
		t.Fatalf("first contact: h2 %d h3 %d, want TCP only", h2.calls, h3.calls)
	}
	get(t, h, "https://api.example/v1")
	if h2.calls != 1 || h3.calls != 1 {
		t.Fatalf("second call: h2 %d h3 %d, want QUIC", h2.calls, h3.calls)
	}
}

func TestAHostThatNeverAdvertisesHTTP3IsNeverTriedOverQUIC(t *testing.T) {
	h2, h3 := &fakeRoundTripper{}, &fakeRoundTripper{}
	h := newColdHybrid(h2, h3)
	for range 10 {
		get(t, h, "https://api.example/v1")
	}
	if h3.calls != 0 || h2.calls != 10 {
		t.Fatalf("h2 %d h3 %d: a host with no advertisement must never wait on QUIC", h2.calls, h3.calls)
	}
}

func TestAnAdvertisementForAnotherPortDoesNotSendThisOneOverQUIC(t *testing.T) {
	h2 := &fakeRoundTripper{reply: advertising(`h3=":8443"; ma=3600`)}
	h3 := &fakeRoundTripper{}
	h := newColdHybrid(h2, h3)
	get(t, h, "https://api.example/v1")
	get(t, h, "https://api.example/v1")
	if h3.calls != 0 {
		t.Fatalf("h3 %d: the advertised port is not the one this request dials", h3.calls)
	}
}

func TestAnAltSvcClearStopsHTTP3(t *testing.T) {
	h2, h3 := &fakeRoundTripper{reply: advertising("clear")}, &fakeRoundTripper{}
	h := newTestHybrid(h2, h3)
	get(t, h, "https://api.example/v1")
	if h3.calls != 1 {
		t.Fatalf("h3 %d: the warm host starts on QUIC", h3.calls)
	}
	h3.reply = advertising("clear")
	get(t, h, "https://api.example/v1")
	get(t, h, "https://api.example/v1")
	if h3.calls != 2 {
		t.Fatalf("h3 %d: a clear must send the host back to TCP", h3.calls)
	}
}

func TestAFailedHostIsNotRelearnedWhileItIsMarked(t *testing.T) {
	h2 := &fakeRoundTripper{reply: advertising(`h3=":443"; ma=3600`)}
	h3 := &fakeRoundTripper{reply: failingReply(errors.New("no quic"))}
	h := newTestHybrid(h2, h3)
	get(t, h, "https://api.example/v1")
	for range 3 {
		get(t, h, "https://api.example/v1")
	}
	if h3.calls != 1 {
		t.Fatalf("h3 %d: the TCP answer re-advertised a host QUIC just failed on", h3.calls)
	}
}

func TestAnAdvertisementExpires(t *testing.T) {
	h2, h3 := &fakeRoundTripper{}, &fakeRoundTripper{}
	h := newColdHybrid(h2, h3)
	h.cache.learn("api.example", time.Now().Add(-time.Second))
	get(t, h, "https://api.example/v1")
	if h3.calls != 0 || h2.calls != 1 {
		t.Fatalf("h2 %d h3 %d: an expired advertisement was honoured", h2.calls, h3.calls)
	}
}

func TestThePlainHTTPAnswerNeverTeachesQUIC(t *testing.T) {
	h2 := &fakeRoundTripper{reply: advertising(`h3=":443"; ma=3600`)}
	h := newColdHybrid(h2, &fakeRoundTripper{})
	get(t, h, "http://api.example/v1")
	if h.cache.usable("api.example") {
		t.Fatal("an advertisement read over clear text was believed")
	}
}
