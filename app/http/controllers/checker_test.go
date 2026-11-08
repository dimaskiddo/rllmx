package controllers

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rllmx/app/network"
	"rllmx/app/upstream"

	"rllmx/tests"
)

// canary is planted in every upstream answer, header and path so a test can
// prove none of it comes back.
const canary = "UPSTREAM-CANARY-7731"

func loopbackChecker(t *testing.T) *Checker {
	t.Helper()
	allow, err := network.ParseAllow("127.0.0.1,localhost")
	if err != nil {
		t.Fatal(err)
	}
	return NewChecker(allow)
}

// upstreamServer answers with status and a body that carries the canary, and
// counts and records what it was asked.
type upstreamServer struct {
	*httptest.Server
	hits    atomic.Int64
	lastKey atomic.Value
	paths   atomic.Value
}

func newUpstream(t *testing.T, status int, body string) *upstreamServer {
	t.Helper()
	u := &upstreamServer{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.hits.Add(1)
		u.lastKey.Store(r.Header.Get("Authorization") + "|" + r.Header.Get("x-api-key") + "|" + r.Header.Get("x-goog-api-key") + "|" + r.URL.RawQuery)
		u.paths.Store(r.Method + " " + r.URL.Path)
		w.Header().Set("X-Canary", canary)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(u.Close)
	return u
}

func entryFor(base string, a *upstream.Auth) upstream.Entry {
	return upstream.Entry{ID: "p", Category: "apikey", Transport: upstream.Transport{BaseURL: base + "/v1/chat/completions"}, Auth: a}
}

func TestRegistryStatusTable(t *testing.T) {
	for _, c := range []struct {
		status int
		want   Outcome
	}{
		{200, OutcomeValid}, {204, OutcomeValid}, {401, OutcomeInvalidKey}, {403, OutcomeInvalidKey},
		{404, OutcomeNotFound}, {500, OutcomeProviderError}, {503, OutcomeProviderError},
		{400, OutcomeUnexpected}, {429, OutcomeUnexpected}, {418, OutcomeUnexpected}, {302, OutcomeUnexpected},
	} {
		srv := newUpstream(t, c.status, canary)
		got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "sk-typed")
		if got.Outcome != c.want {
			t.Errorf("status %d: outcome %d, want %d", c.status, got.Outcome, c.want)
		}
	}
}

func TestACustomBadKeySetReplacesTheDefault(t *testing.T) {
	srv := newUpstream(t, 400, "")
	a := &upstream.Auth{Scheme: "bearer", Header: "Authorization", BadKey: []int{400}}
	if got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, a), "k"); got.Outcome != OutcomeInvalidKey {
		t.Fatalf("outcome %d", got.Outcome)
	}
	srv2 := newUpstream(t, 401, "")
	if got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv2.URL, a), "k"); got.Outcome == OutcomeInvalidKey {
		t.Fatal("a listed set must replace 401/403, not add to them")
	}
}

func TestEachSchemePresentsTheKeyWhereTheDataSays(t *testing.T) {
	for name, tc := range map[string]struct {
		auth *upstream.Auth
		want string
	}{
		"bearer": {&upstream.Auth{Scheme: "bearer", Header: "Authorization"}, "Bearer sk-typed|||"},
		"raw":    {&upstream.Auth{Scheme: "raw", Header: "x-api-key"}, "|sk-typed||"},
		"query":  {&upstream.Auth{Scheme: "query", Header: "key"}, "|||key=sk-typed"},
	} {
		srv := newUpstream(t, 200, "")
		loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, tc.auth), "sk-typed")
		if got, _ := srv.lastKey.Load().(string); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestStaticHeadersAreSentWithTheProbe(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("anthropic-version") }))
	defer srv.Close()
	a := &upstream.Auth{Scheme: "raw", Header: "x-api-key", Headers: map[string]string{"anthropic-version": "2023-06-01"}}
	loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, a), "k")
	if got != "2023-06-01" {
		t.Fatalf("%q", got)
	}
}

func TestAnEmptyKeyMakesNoRequest(t *testing.T) {
	srv := newUpstream(t, 200, "")
	ck := loopbackChecker(t)
	for _, key := range []string{"", "   ", "\t"} {
		if got := ck.CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), key); got.Outcome != OutcomeInvalidKey {
			t.Errorf("registry %q: %d", key, got.Outcome)
		}
		if got := ck.CheckNode(context.Background(), Node{Type: upstream.NodeTypeOpenAI, BaseURL: srv.URL}, key, "", false); got.Outcome != OutcomeInvalidKey {
			t.Errorf("node %q: %d", key, got.Outcome)
		}
	}
	if srv.hits.Load() != 0 {
		t.Fatalf("an empty key made %d requests", srv.hits.Load())
	}
}

func TestAProviderWithoutAnAuthBlockIsNotSupportedAndMakesNoRequest(t *testing.T) {
	srv := newUpstream(t, 200, "")
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, nil), "k")
	if got.Outcome != OutcomeUnsupported || srv.hits.Load() != 0 {
		t.Fatalf("outcome %d, hits %d", got.Outcome, srv.hits.Load())
	}
}

func TestNothingFromTheUpstreamIsEverReturned(t *testing.T) {
	for _, status := range []int{200, 401, 404, 500, 400} {
		srv := newUpstream(t, status, `{"error":"`+canary+`","key":"`+canary+`"}`)
		got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "k")
		raw, _ := json.Marshal(got)
		text := string(raw) + got.Outcome.Message()
		for _, leak := range []string{canary, srv.URL, "127.0.0.1", fmt.Sprint(status)} {
			if got.Outcome != OutcomeValid && strings.Contains(got.Outcome.Message(), leak) {
				t.Errorf("status %d: the message %q carries %q", status, text, leak)
			}
		}
	}
}

func TestEveryOutcomeHasOneFixedMessage(t *testing.T) {
	seen := map[string]Outcome{}
	for o := OutcomeInvalidKey; o <= OutcomeUnsupported; o++ {
		m := o.Message()
		if m == "" {
			t.Errorf("outcome %d has no message", o)
		}
		if prev, dup := seen[m]; dup {
			t.Errorf("outcomes %d and %d share %q", prev, o, m)
		}
		seen[m] = o
	}
	if OutcomeValid.Message() != "" {
		t.Error("a valid outcome has a message")
	}
}

func TestARedirectToAnotherAddressIsRefusedAndTheKeyIsNeverForwarded(t *testing.T) {
	second := newUpstream(t, 200, "")
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+"/models", http.StatusFound)
	}))
	defer first.Close()
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(first.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "sk-typed")
	if got.Outcome != OutcomeRedirected {
		t.Fatalf("outcome %d", got.Outcome)
	}
	if second.hits.Load() != 0 {
		t.Fatalf("the redirect target received %d requests, key %q", second.hits.Load(), second.lastKey.Load())
	}
}

func TestARedirectWithinTheSameOriginIsFollowed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/v1/models2", http.StatusFound) })
	mux.HandleFunc("/v1/models2", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	a := &upstream.Auth{Scheme: "bearer", Header: "Authorization"}
	if got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, a), "k"); got.Outcome != OutcomeValid {
		t.Fatalf("outcome %d", got.Outcome)
	}
}

func TestAnotherPortIsAnotherAddress(t *testing.T) {
	second := newUpstream(t, 200, "")
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, strings.Replace(second.URL, "127.0.0.1", "localhost", 1)+"/models", http.StatusFound)
	}))
	defer first.Close()
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(first.URL, &upstream.Auth{Scheme: "raw", Header: "x-api-key"}), "sk-typed")
	if got.Outcome != OutcomeRedirected || second.hits.Load() != 0 {
		t.Fatalf("outcome %d, hits %d", got.Outcome, second.hits.Load())
	}
}

func TestALoopbackServerNeedsTheAllowList(t *testing.T) {
	srv := newUpstream(t, 200, "")
	got := NewChecker(nil).CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "k")
	if got.Outcome != OutcomeBlocked || srv.hits.Load() != 0 {
		t.Fatalf("outcome %d, hits %d: the guard let a loopback address through", got.Outcome, srv.hits.Load())
	}
}

func TestABodyOverTheCapIsTooLarge(t *testing.T) {
	srv := newUpstream(t, 200, strings.Repeat("a", 1<<20+10))
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "k")
	if got.Outcome != OutcomeTooLarge {
		t.Fatalf("outcome %d", got.Outcome)
	}
}

func TestASlowUpstreamTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	ck := loopbackChecker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := ck.CheckRegistry(ctx, entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "k")
	if got.Outcome != OutcomeTimedOut {
		t.Fatalf("outcome %d", got.Outcome)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the check outlived its context")
	}
}

func TestACertificateThatIsNotTrustedIsReportedAsSuch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "k")
	if got.Outcome != OutcomeCertificate {
		t.Fatalf("outcome %d", got.Outcome)
	}
}

func TestAnUnreachableHostCouldNotConnect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close()
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(base, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "k")
	if got.Outcome != OutcomeNoConnect {
		t.Fatalf("outcome %d", got.Outcome)
	}
}

func TestOpenAINodeProbe(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/v1/models":
			w.WriteHeader(404)
		case r.URL.Path == "/v1/chat/completions" && r.Header.Get("Authorization") == "Bearer good":
			w.WriteHeader(200)
		default:
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	ck := loopbackChecker(t)
	n := Node{Type: upstream.NodeTypeOpenAI, BaseURL: srv.URL + "/v1"}

	if got := ck.CheckNode(context.Background(), n, "good", "", false); got.Outcome != OutcomeNotFound {
		t.Errorf("no model: %d", got.Outcome)
	}
	calls = nil
	got := ck.CheckNode(context.Background(), n, "good", "m", false)
	if got.Outcome != OutcomeValid || got.Method != "chat" || len(calls) != 2 {
		t.Errorf("with a model: %+v %v", got, calls)
	}
	if got := ck.CheckNode(context.Background(), n, "bad", "m", false); got.Outcome != OutcomeInvalidKey {
		t.Errorf("bad key: %d", got.Outcome)
	}
}

func TestAnAuthFailureOnModelsStopsBeforeAnyChat(t *testing.T) {
	srv := newUpstream(t, 401, "")
	got := loopbackChecker(t).CheckNode(context.Background(), Node{Type: upstream.NodeTypeOpenAI, BaseURL: srv.URL}, "k", "m", false)
	if got.Outcome != OutcomeInvalidKey || srv.hits.Load() != 1 {
		t.Fatalf("outcome %d after %d requests", got.Outcome, srv.hits.Load())
	}
}

func TestAnthropicNodeProbe(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/models" {
			w.WriteHeader(404)
			return
		}
		if r.Header.Get("x-api-key") == "good" {
			w.WriteHeader(400) // a body problem still proves the key
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	ck := loopbackChecker(t)
	n := Node{Type: upstream.NodeTypeAnthropic, BaseURL: srv.URL}
	if got := ck.CheckNode(context.Background(), n, "good", "", false); got.Outcome != OutcomeNotFound {
		t.Errorf("unsaved key never sends a message: %d %v", got.Outcome, calls)
	}
	calls = nil
	if got := ck.CheckNode(context.Background(), n, "good", "", true); got.Outcome != OutcomeValid || got.Method != "messages" {
		t.Errorf("saved good key: %+v %v", got, calls)
	}
	if got := ck.CheckNode(context.Background(), n, "bad", "", true); got.Outcome != OutcomeInvalidKey {
		t.Errorf("saved bad key: %d", got.Outcome)
	}
}

func TestEmbeddingNodeProbeReportsDimensionsNotTheVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`)
	}))
	defer srv.Close()
	got := loopbackChecker(t).CheckNode(context.Background(), Node{Type: upstream.NodeTypeEmbedding, BaseURL: srv.URL}, "k", "emb-1", false)
	if got.Outcome != OutcomeValid || got.Dimensions != 4 || got.Method != "embeddings" {
		t.Fatalf("%+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "0.1") {
		t.Fatalf("the vector reached the result: %s", raw)
	}
}

func TestNodeModelListShapesAndCaps(t *testing.T) {
	big := make([]map[string]string, 0, 700)
	for i := 0; i < 700; i++ {
		big = append(big, map[string]string{"id": fmt.Sprintf("m%d", i)})
	}
	bigJSON, _ := json.Marshal(map[string]any{"data": big})
	for name, tc := range map[string]struct {
		body string
		want int
	}{
		"data wrapper":   {`{"data":[{"id":"a"},{"id":"b","name":"B"}]}`, 2},
		"models wrapper": {`{"models":[{"id":"a"}]}`, 1},
		"results":        {`{"results":[{"id":"a"}]}`, 1},
		"bare array":     {`[{"id":"a"},{"id":"b"},{"id":"c"}]`, 3},
		"control in id":  {"{\"data\":[{\"id\":\"a\\u0085b\"},{\"id\":\"ok\"}]}", 1},
		"id too long":    {`{"data":[{"id":"` + strings.Repeat("x", 257) + `"},{"id":"ok"}]}`, 1},
		"no id":          {`{"data":[{"name":"x"},{"id":"ok"}]}`, 1},
		"capped at 500":  {string(bigJSON), 500},
	} {
		srv := newUpstream(t, 200, tc.body)
		list, out := loopbackChecker(t).ListNodeModels(context.Background(), Node{Type: upstream.NodeTypeOpenAI, BaseURL: srv.URL}, "k")
		if out != OutcomeValid || len(list) != tc.want {
			t.Errorf("%s: outcome %d, %d models, want %d", name, out, len(list), tc.want)
		}
	}
	srv := newUpstream(t, 200, `not json`)
	if _, out := loopbackChecker(t).ListNodeModels(context.Background(), Node{Type: upstream.NodeTypeOpenAI, BaseURL: srv.URL}, "k"); out != OutcomeUnexpected {
		t.Errorf("not json: %d", out)
	}
	srv = newUpstream(t, 401, "")
	if _, out := loopbackChecker(t).ListNodeModels(context.Background(), Node{Type: upstream.NodeTypeOpenAI, BaseURL: srv.URL}, "k"); out != OutcomeInvalidKey {
		t.Errorf("401: %d", out)
	}
}

func TestAHopFromHTTPToHTTPSOnTheSameHostAndPortIsAnotherAddress(t *testing.T) {
	// The shared client only refuses an https-to-http downgrade. The upgrade is
	// still another scheme, and a typed key must not follow it.
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "https://"+r.Host+"/models", http.StatusFound)
	}))
	defer srv.Close()
	got := loopbackChecker(t).CheckRegistry(context.Background(), entryFor(srv.URL, &upstream.Auth{Scheme: "bearer", Header: "Authorization"}), "sk-typed")
	if got.Outcome != OutcomeRedirected {
		t.Fatalf("outcome %d", got.Outcome)
	}
	if hits.Load() != 1 {
		t.Fatalf("%d requests: the redirect was followed", hits.Load())
	}
}

func TestClassifyErrorByType(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want Outcome
	}{
		"redirect":        {fmt.Errorf("wrapped: %w", errRedirected), OutcomeRedirected},
		"blocked":         {fmt.Errorf("dial: %w", network.ErrBlocked), OutcomeBlocked},
		"unknown ca":      {x509.UnknownAuthorityError{}, OutcomeCertificate},
		"wrong host":      {x509.HostnameError{}, OutcomeCertificate},
		"expired cert":    {x509.CertificateInvalidError{Reason: x509.Expired}, OutcomeCertificate},
		"deadline":        {context.DeadlineExceeded, OutcomeTimedOut},
		"net timeout":     {tests.TimeoutError{}, OutcomeTimedOut},
		"anything else":   {fmt.Errorf("connection refused at 10.0.0.1:443"), OutcomeNoConnect},
		"a url error too": {&url.Error{Op: "Get", URL: "https://secret.example/v1?key=SECRET", Err: fmt.Errorf("boom")}, OutcomeNoConnect},
	} {
		if got := classifyError(tc.err); got != tc.want {
			t.Errorf("%s: %d, want %d", name, got, tc.want)
		}
	}
}
