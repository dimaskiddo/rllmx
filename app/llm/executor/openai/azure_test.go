package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

type capture struct{ got *http.Request }

func (c *capture) RoundTrip(r *http.Request) (*http.Response, error) {
	c.got = r
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{}}, nil
}

func azureRun(t *testing.T, kv map[string]string, key string) (*http.Request, error) {
	t.Helper()
	cap := &capture{}
	a := &Azure{Default: &executor.Default{Client: &http.Client{Transport: cap}}}
	resp, err := a.Execute(context.Background(), executor.Request{
		Entry: upstream.Entry{Executor: upstream.ExecutorAzure}, Key: key, Model: "gpt-4o", Body: []byte(`{}`), Account: tests.Account(kv),
	})
	if err == nil {
		resp.Body.Close()
	}
	return cap.got, err
}

func TestAzureBuildsItsAddressFromTheAccountAndSendsTheKeyInApiKey(t *testing.T) {
	r, err := azureRun(t, map[string]string{"azureEndpoint": "https://res.openai.azure.com/", "deployment": "prod", "apiVersion": "2025-01-01", "organization": "org-1"}, llm.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.URL.String(); got != "https://res.openai.azure.com/openai/deployments/prod/chat/completions?api-version=2025-01-01" {
		t.Fatal(got)
	}
	if r.Header.Get("api-key") != llm.Secret || r.Header.Get("Authorization") != "" || r.Header.Get("OpenAI-Organization") != "org-1" {
		t.Fatalf("headers %v", r.Header)
	}
}

func TestAzureDefaultsTheDeploymentToTheModelAndTheVersionToTheBuiltInOne(t *testing.T) {
	r, err := azureRun(t, map[string]string{"azureEndpoint": "https://res.openai.azure.com"}, llm.Secret)
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
		r, err := azureRun(t, kv, llm.Secret)
		if !errors.Is(err, executor.ErrBadAccount) || r != nil {
			t.Errorf("%s: err %v, request sent %v", name, err, r != nil)
		}
	}
	if _, err := azureRun(t, good, ""); !errors.Is(err, executor.ErrNoCredential) {
		t.Fatalf("no key: %v", err)
	}
}
