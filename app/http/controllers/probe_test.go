package controllers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"rllmx/app/llm/upstream"
	"rllmx/app/platform/network"
)

// probeRig serves one status and records the request it received.
type probeRig struct {
	*httptest.Server
	status int
	method string
	path   string
	auth   string
	body   string
}

func newProbeRig(t *testing.T, status int) *probeRig {
	t.Helper()
	r := &probeRig{status: status}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		r.method, r.path, r.auth, r.body = req.Method, req.URL.Path, req.Header.Get("Authorization"), string(raw)
		w.WriteHeader(r.status)
	}))
	t.Cleanup(r.Close)
	return r
}

func probeEntry(r *probeRig, p *upstream.Probe, scheme, header string) upstream.Entry {
	return upstream.Entry{
		Transport: upstream.Transport{BaseURL: r.URL + "/v1/chat/completions"},
		Auth:      &upstream.Auth{Scheme: scheme, Header: header, Probe: p},
	}
}

func probeChecker(t *testing.T) *Checker {
	t.Helper()
	allow, err := network.ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	return NewChecker(allow)
}

func TestAProbeSendsItsOwnMethodPathAndBody(t *testing.T) {
	r := newProbeRig(t, http.StatusOK)
	e := probeEntry(r, &upstream.Probe{Method: "POST", Path: "/api/v1/exchange", Body: `{"a":1}`}, "bearer", "Authorization")
	res := probeChecker(t).CheckRegistry(context.Background(), e, "KEY")
	if !res.Valid() || res.Method != "probe" {
		t.Fatalf("%+v", res)
	}
	if r.method != http.MethodPost || r.path != "/api/v1/exchange" || r.auth != "Bearer KEY" || r.body != `{"a":1}` {
		t.Fatalf("%s %s %q %q", r.method, r.path, r.auth, r.body)
	}
}

func TestAProbeJudgesItsStatusLists(t *testing.T) {
	cases := []struct {
		name   string
		status int
		probe  upstream.Probe
		want   Outcome
		warn   bool
	}{
		{"a 2xx passes", 200, upstream.Probe{}, OutcomeValid, false},
		{"an ok status passes", 404, upstream.Probe{OK: []int{404}}, OutcomeValid, false},
		{"a soft status warns and passes", 402, upstream.Probe{Soft: []int{402}}, OutcomeValid, true},
		{"a refusal fails", 401, upstream.Probe{}, OutcomeInvalidKey, false},
		{"a listed bad key fails", 418, upstream.Probe{OK: []int{404}}, OutcomeUnexpected, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newProbeRig(t, c.status)
			res := probeChecker(t).CheckRegistry(context.Background(), probeEntry(r, &c.probe, "bearer", "Authorization"), "KEY")
			if res.Outcome != c.want || res.Warning != c.warn {
				t.Fatalf("outcome %v warning %v, want %v %v", res.Outcome, res.Warning, c.want, c.warn)
			}
		})
	}
}

func TestAKeylessProbeSendsNoCredential(t *testing.T) {
	r := newProbeRig(t, http.StatusOK)
	res := probeChecker(t).CheckRegistry(context.Background(), probeEntry(r, &upstream.Probe{Method: "GET", Path: "/api/tags"}, "", ""), "")
	if res.Outcome != OutcomeInvalidKey {
		t.Fatalf("an empty key was checked: %v", res.Outcome)
	}
}
