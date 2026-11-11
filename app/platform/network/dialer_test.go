package network

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// listenerOn starts a server on an address the test chose, so a private address
// can be exercised.
func listenerOn(t *testing.T, addr string, h nethttp.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestTheGuardRefusesAPrivateAddressWithoutAListEntry(t *testing.T) {
	srv := listenerOn(t, "127.0.0.1:0", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	g := NewDialer(time.Second, nil)
	if _, err := g.DialContext(context.Background(), "tcp", srv.Listener.Addr().String()); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err %v, want ErrBlocked", err)
	}
}

func TestTheGuardDialsAListedAddress(t *testing.T) {
	srv := listenerOn(t, "127.0.0.1:0", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	allow, err := ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := NewDialer(time.Second, allow).DialContext(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestTheGuardRefusesAMetadataAddressEvenWhenListed(t *testing.T) {
	allow, err := ParseAllow("169.254.169.254,169.254.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	g := NewDialer(time.Second, allow)
	if _, err := g.DialContext(context.Background(), "tcp", "169.254.169.254:80"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("err %v: the metadata address must be refused", err)
	}
}

func TestAListedNameMayResolveToAPrivateAddress(t *testing.T) {
	// "localhost" resolves to a loopback address, which is Internal. Listing the
	// name is what permits it, and the dial then goes to 127.0.0.1.
	srv := listenerOn(t, "127.0.0.1:0", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	unlisted := NewDialer(time.Second, nil)
	if _, err := unlisted.DialContext(context.Background(), "tcp", "localhost:"+port); !errors.Is(err, ErrBlocked) {
		t.Fatalf("an unlisted name reached a loopback address: %v", err)
	}

	allow, err := ParseAllow("localhost")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := NewDialer(time.Second, allow).DialContext(context.Background(), "tcp", "localhost:"+port)
	if err != nil {
		t.Fatalf("a listed name was refused: %v", err)
	}
	_ = conn.Close()
}

func TestAListedNameThatResolvesToTheMetadataAddressIsRefused(t *testing.T) {
	allow, err := ParseAllow("idp.internal.example")
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"169.254.169.254", "fe80::1", "::ffff:169.254.169.254", "64:ff9b::a9fe:a9fe"} {
		g := NewDialer(time.Second, allow)
		ip := netip.MustParseAddr(answer)
		g.lookup = func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{ip}, nil }
		if _, _, err := g.targets(context.Background(), "idp.internal.example:443"); !errors.Is(err, ErrBlocked) {
			t.Errorf("%s: err %v, want ErrBlocked", answer, err)
		}
	}
}

func TestAListedNameKeepsItsInternalAnswerAndDropsTheNeverOne(t *testing.T) {
	allow, err := ParseAllow("idp.internal.example")
	if err != nil {
		t.Fatal(err)
	}
	g := NewDialer(time.Second, allow)
	g.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254"), netip.MustParseAddr("10.1.2.3")}, nil
	}
	_, out, err := g.targets(context.Background(), "idp.internal.example:443")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0] != "10.1.2.3:443" {
		t.Fatalf("targets %v, want only 10.1.2.3:443", out)
	}
}

func TestTheGuardRefusesAMalformedTarget(t *testing.T) {
	g := NewDialer(time.Second, nil)
	for _, target := range []string{"no-port", "127.0.0.1:http", ":80"} {
		if _, err := g.DialContext(context.Background(), "tcp", target); err == nil {
			t.Errorf("%q dialled", target)
		}
	}
}

func TestTheClientFollowsTheRedirectLimit(t *testing.T) {
	var hits int
	srv := listenerOn(t, "127.0.0.1:0", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		hits++
		if r.URL.Path == "/" {
			w.Header().Set("Location", "/one")
			w.WriteHeader(nethttp.StatusFound)
			return
		}
		_, _ = w.Write([]byte("done"))
	}))
	allow, err := ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(Options{ConnectTimeout: time.Second, MaxRedirects: 3, Allow: allow})
	resp, err := c.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusOK || hits != 2 {
		t.Fatalf("%d after %d hits", resp.StatusCode, hits)
	}
}

func TestTheClientStopsAtTheLimit(t *testing.T) {
	srv := listenerOn(t, "127.0.0.1:0", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		// Every request redirects one hop further, so the limit is what stops it.
		w.Header().Set("Location", r.URL.Path+"x")
		w.WriteHeader(nethttp.StatusFound)
	}))
	allow, _ := ParseAllow("127.0.0.1")
	c := NewClient(Options{ConnectTimeout: time.Second, MaxRedirects: -1, Allow: allow})
	resp, err := c.Get(srv.URL + "/a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusFound {
		t.Fatalf("%d: a negative limit followed a redirect", resp.StatusCode)
	}
}

func TestTheClientFollowsNoRedirectToPlainHTTP(t *testing.T) {
	// A TLS server redirecting to http must not be followed, because the next hop
	// would carry the request in clear text.
	target := listenerOn(t, "127.0.0.1:0", nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		_, _ = w.Write([]byte("plain"))
	}))
	_, plainPort, _ := net.SplitHostPort(target.Listener.Addr().String())

	tlsSrv := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.Header().Set("Location", "http://127.0.0.1:"+plainPort+"/")
		w.WriteHeader(nethttp.StatusFound)
	}))
	defer tlsSrv.Close()

	allow, _ := ParseAllow("127.0.0.1")
	c := NewClient(Options{ConnectTimeout: time.Second, MaxRedirects: 3, Allow: allow, TLS: tlsSrv.Client().Transport.(*nethttp.Transport).TLSClientConfig})
	resp, err := c.Get(tlsSrv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != nethttp.StatusFound {
		t.Fatalf("%d: an https-to-http redirect was followed", resp.StatusCode)
	}
}

func TestTheTransportReadsNoProxy(t *testing.T) {
	for _, v := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(v, "http://127.0.0.1:1")
	}
	tr := NewTransport(Options{})
	if tr.Proxy != nil {
		t.Fatal("the transport reads a proxy variable")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("the transport does not verify TLS")
	}
	if tr.TLSClientConfig.MinVersion < 0x0303 {
		t.Fatalf("min version %x, want TLS 1.2 or newer", tr.TLSClientConfig.MinVersion)
	}
}

func TestTheGuardDescriptionNamesNoTarget(t *testing.T) {
	allow, _ := ParseAllow("idp.internal.example")
	g := NewDialer(time.Second, allow)
	if got := g.String(); strings.Contains(got, "idp.internal") {
		t.Fatalf("the description names an entry: %q", got)
	}
	if got := NewDialer(time.Second, nil).String(); !strings.Contains(got, "refused") {
		t.Fatalf("description %q", got)
	}
}

func TestTheGuardRacesItsAddressesWithFastFallback(t *testing.T) {
	// The first address never answers; the second is reached after one
	// fallback delay rather than after the first one's whole timeout.
	var dialed []string
	g := NewDialer(10*time.Second, nil)
	g.allow = mustAllow(t, "public.example")
	g.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}, nil
	}
	reached := make(chan time.Duration, 1)
	start := time.Now()
	g.dial = func(ctx context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if strings.HasPrefix(addr, "10.0.0.1:") {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		client, server := net.Pipe()
		defer server.Close()
		reached <- time.Since(start)
		return client, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := g.DialContext(ctx, "tcp", "public.example:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_ = conn.Close()
	elapsed := <-reached
	if elapsed < fallbackDelay/2 || elapsed > fallbackDelay*3 {
		t.Fatalf("the second address was reached after %s, want about %s", elapsed, fallbackDelay)
	}
	if len(dialed) != 2 {
		t.Fatalf("dialed %v", dialed)
	}
}

func TestTheGuardReturnsTheLastErrorWhenEveryAddressFails(t *testing.T) {
	g := NewDialer(time.Second, nil)
	g.allow = mustAllow(t, "public.example")
	g.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}, nil
	}
	g.dial = func(_ context.Context, _, addr string) (net.Conn, error) {
		return nil, errors.New("refused " + addr)
	}
	_, err := g.DialContext(context.Background(), "tcp", "public.example:443")
	if err == nil || !strings.Contains(err.Error(), "10.0.0.") {
		t.Fatalf("err = %v, want the last dial's error", err)
	}
}

func TestACancelledRaceEndsPromptly(t *testing.T) {
	g := NewDialer(time.Minute, nil)
	g.allow = mustAllow(t, "public.example")
	g.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}, nil
	}
	g.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := g.DialContext(ctx, "tcp", "public.example:443"); err == nil {
		t.Fatal("a cancelled dial succeeded")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a cancelled race took %s", elapsed)
	}
}

func TestTheTransportOffersOnlyTheCurveItWasGiven(t *testing.T) {
	tr := NewTransport(Options{Curves: []tls.CurveID{tls.X25519}})
	if got := tr.TLSClientConfig.CurvePreferences; len(got) != 1 || got[0] != tls.X25519 {
		t.Fatalf("curves %v", got)
	}
	if got := NewTransport(Options{}).TLSClientConfig.CurvePreferences; len(got) != 0 {
		t.Fatalf("the default offer was narrowed: %v", got)
	}
}

// mustAllow is the allow list a race test uses to reach a private literal.
func mustAllow(t *testing.T, hosts ...string) *Allow {
	t.Helper()
	a, err := ParseAllow(strings.Join(hosts, ","))
	if err != nil {
		t.Fatal(err)
	}
	return a
}
