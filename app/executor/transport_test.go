package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/upstream"
	"rllmx/tests"
)

func account(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestAProvidersRetryRuleBeatsTheBuiltInTable(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 429}, tests.Reply{Status: 429}, tests.Reply{Status: 429}, tests.Reply{Body: `{}`})
	e := upstream.Entry{Transport: upstream.Transport{Retry: map[int]upstream.Retry{429: {Attempts: 2, DelayMs: 250}}}}
	var waits []time.Duration
	resp, err := run(t, &Default{Sleep: instant(&waits)}, Request{URL: up.URL, Key: secret, Auth: bearerAuth(), Entry: e})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Status != 429 || len(up.Calls()) != 3 {
		t.Fatalf("status %d after %d calls, want 429 after 3", resp.Status, len(up.Calls()))
	}
	if len(waits) != 2 || waits[0] != 250*time.Millisecond {
		t.Fatalf("waits %v", waits)
	}
}

func TestAnotherProvidersRetryRuleDoesNotLeakAndTheBuiltInOnesStay(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 429}, tests.Reply{Body: `{}`})
	var waits []time.Duration
	resp, err := run(t, &Default{Sleep: instant(&waits)}, Request{URL: up.URL, Key: secret, Auth: bearerAuth()})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Status != 429 || len(up.Calls()) != 1 {
		t.Fatalf("a 429 was retried in place: %d calls", len(up.Calls()))
	}
	up = tests.NewUpstream(t, tests.Reply{Status: 503}, tests.Reply{Body: `{}`})
	e := upstream.Entry{Transport: upstream.Transport{Retry: map[int]upstream.Retry{429: {Attempts: 2}}}}
	resp, err = run(t, &Default{Sleep: instant(&waits)}, Request{URL: up.URL, Key: secret, Auth: bearerAuth(), Entry: e})
	if err != nil || resp.Status != 200 {
		t.Fatalf("the 503 rule was lost: %v %d", err, resp.Status)
	}
	resp.Body.Close()
}

func TestTransportHeadersAreSentAndCannotReplaceTheKey(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	e := upstream.Entry{Transport: upstream.Transport{Headers: map[string]string{"X-Title": "t", "Authorization": "Bearer stolen"}}}
	resp, err := run(t, &Default{}, Request{URL: up.URL, Key: secret, Auth: bearerAuth(), Entry: e})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := up.Calls()[0].Header
	if h.Get("X-Title") != "t" || h.Get("Authorization") != "Bearer "+secret {
		t.Fatalf("headers %v", h)
	}
}

func TestTheURLSuffixAppliesOnceAndOnlyWhereThereIsNoQuery(t *testing.T) {
	e := upstream.Entry{Transport: upstream.Transport{BaseURL: "https://a.example/v1/messages", URLSuffix: "?beta=true"}}
	if got := RegistryURL(e, upstream.FormatClaude, "m", false); got != "https://a.example/v1/messages?beta=true" {
		t.Fatal(got)
	}
	e.Transport.BaseURL += "?x=1"
	if got := RegistryURL(e, upstream.FormatClaude, "m", false); got != "https://a.example/v1/messages?x=1" {
		t.Fatalf("a second query was added: %s", got)
	}
}

func TestTheAccountIDFillsTheAddressOrNothingIsSent(t *testing.T) {
	const addr = "/client/v4/accounts/{accountId}/ai/v1/chat/completions"
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	resp, err := run(t, &Default{}, Request{URL: up.URL + addr, Key: secret, Auth: bearerAuth(), Account: account(map[string]string{"accountId": "abc123"})})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := up.Calls()[0].Path; got != "/client/v4/accounts/abc123/ai/v1/chat/completions" {
		t.Fatalf("path %s", got)
	}
	for name, id := range map[string]string{"missing": "", "slash": "a/b", "dots": "..", "query": "a?b", "space": "a b", "newline": "a\nb", "long": strings.Repeat("a", 129)} {
		before := len(up.Calls())
		_, err := run(t, &Default{}, Request{URL: up.URL + addr, Key: secret, Auth: bearerAuth(), Account: account(map[string]string{"accountId": id})})
		if !errors.Is(err, ErrBadAccount) || len(up.Calls()) != before {
			t.Errorf("%s: err %v, %d calls sent", name, err, len(up.Calls())-before)
		}
	}
	if _, err := run(t, &Default{}, Request{URL: up.URL + addr, Key: secret, Auth: bearerAuth()}); !errors.Is(err, ErrBadAccount) {
		t.Fatalf("no account reader: %v", err)
	}
}

func TestTheBaseURLFillsTheAddressFromTheAccountOrNothingIsSent(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	addr := upstream.BaseURLToken + "/api/v4/chat/completions"
	// the test server is plain http, so a bare https origin can only be checked
	// by the refusal cases here and by the fill rule below
	for name, base := range map[string]string{
		"missing": "", "plain http": up.URL, "a path": "https://git.example/sub", "userinfo": "https://u:p@git.example",
		"a query": "https://git.example?x=1", "a trailing slash": "https://git.example/", "a space": "https://git .example",
	} {
		before := len(up.Calls())
		_, err := run(t, &Default{}, Request{URL: addr, Key: secret, Auth: bearerAuth(), Account: account(map[string]string{"baseUrl": base})})
		if !errors.Is(err, ErrBadAccount) || len(up.Calls()) != before {
			t.Errorf("%s: err %v, %d calls sent", name, err, len(up.Calls())-before)
		}
	}
	if _, err := run(t, &Default{}, Request{URL: addr, Key: secret, Auth: bearerAuth()}); !errors.Is(err, ErrBadAccount) {
		t.Fatalf("no account reader: %v", err)
	}
	got, err := fillAccount(Request{URL: addr, Account: account(map[string]string{"baseUrl": "https://git.example:8443"})})
	if err != nil || got != "https://git.example:8443/api/v4/chat/completions" {
		t.Fatalf("%q %v", got, err)
	}
}

type capture struct{ got *http.Request }

func (c *capture) RoundTrip(r *http.Request) (*http.Response, error) {
	c.got = r
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
}

func azureRun(t *testing.T, kv map[string]string, key string) (*http.Request, error) {
	t.Helper()
	cap := &capture{}
	m := NewMux(&Default{Client: &http.Client{Transport: cap}}, Options{})
	resp, err := m.Execute(context.Background(), Request{
		Entry: upstream.Entry{Executor: upstream.ExecutorAzure}, Key: key, Model: "gpt-4o", Body: []byte(`{}`), Account: account(kv),
	})
	if err == nil {
		resp.Body.Close()
	}
	return cap.got, err
}

func TestAzureBuildsItsAddressFromTheAccountAndSendsTheKeyInApiKey(t *testing.T) {
	r, err := azureRun(t, map[string]string{"azureEndpoint": "https://res.openai.azure.com/", "deployment": "prod", "apiVersion": "2025-01-01", "organization": "org-1"}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.URL.String(); got != "https://res.openai.azure.com/openai/deployments/prod/chat/completions?api-version=2025-01-01" {
		t.Fatal(got)
	}
	if r.Header.Get("api-key") != secret || r.Header.Get("Authorization") != "" || r.Header.Get("OpenAI-Organization") != "org-1" {
		t.Fatalf("headers %v", r.Header)
	}
}

func TestAzureDefaultsTheDeploymentToTheModelAndTheVersionToTheBuiltInOne(t *testing.T) {
	r, err := azureRun(t, map[string]string{"azureEndpoint": "https://res.openai.azure.com"}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.URL.String(); got != "https://res.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version="+defaultAzureVersion {
		t.Fatal(got)
	}
}

func TestAzureRefusesAnythingThatCouldRedirectTheKey(t *testing.T) {
	good := map[string]string{"azureEndpoint": "https://res.openai.azure.com", "deployment": "prod"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range good {
			m[a] = b
		}
		m[k] = v
		return m
	}
	bad := map[string]map[string]string{
		"no endpoint":      {"deployment": "prod"},
		"plain http":       with("azureEndpoint", "http://res.openai.azure.com"),
		"credentials":      with("azureEndpoint", "https://u:p@res.openai.azure.com"),
		"a path":           with("azureEndpoint", "https://res.openai.azure.com/evil"),
		"a query":          with("azureEndpoint", "https://res.openai.azure.com?x=1"),
		"deployment slash": with("deployment", "a/b"),
		"deployment query": with("deployment", "a?b"),
		"version injected": with("apiVersion", "1&key=x"),
		"org newline":      with("organization", "a\nb"),
	}
	for name, kv := range bad {
		r, err := azureRun(t, kv, secret)
		if !errors.Is(err, ErrBadAccount) || r != nil {
			t.Errorf("%s: err %v, request sent %v", name, err, r != nil)
		}
	}
	if _, err := azureRun(t, good, ""); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("no key: %v", err)
	}
}

func TestAnUnregisteredExecutorFailsTheCallAndNeverFallsBack(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	m := NewMux(&Default{Client: http.DefaultClient}, Options{})
	_, err := m.Execute(context.Background(), Request{URL: up.URL, Key: secret, Auth: bearerAuth(), Entry: upstream.Entry{Executor: "mystery"}})
	if !errors.Is(err, ErrUnknownExecutor) || len(up.Calls()) != 0 {
		t.Fatalf("err %v after %d calls", err, len(up.Calls()))
	}
	resp, err := m.Execute(context.Background(), Request{URL: up.URL, Key: secret, Auth: bearerAuth()})
	if err != nil {
		t.Fatalf("a request with no entry (a user node) must use the default executor: %v", err)
	}
	resp.Body.Close()
}

func TestTheMuxServesExactlyTheExecutorsADataFileMayName(t *testing.T) {
	got := map[string]bool{}
	for _, n := range NewMux(&Default{}, Options{}).Names() {
		got[n] = true
	}
	want := upstream.ExecutorNames()
	if len(got) != len(want) {
		t.Fatalf("mux %v, data files may name %v", got, want)
	}
	for _, n := range want {
		if !got[n] {
			t.Errorf("a data file may name %q and the mux has no such executor", n)
		}
	}
}
