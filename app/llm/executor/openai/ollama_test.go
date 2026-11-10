package openai

import (
	"context"
	"errors"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/tests"
)

func TestOllamaAddressIsTheAccountOwnServer(t *testing.T) {
	for name, tc := range map[string]struct {
		acct func(string) string
		want string
	}{
		"no account data":  {nil, "http://localhost:11434/api/chat"},
		"an empty address": {tests.Account(map[string]string{"baseUrl": "   "}), "http://localhost:11434/api/chat"},
		"a trailing slash": {tests.Account(map[string]string{"baseUrl": "http://192.168.1.9:11434/"}), "http://192.168.1.9:11434/api/chat"},
		"a saved host":     {tests.Account(map[string]string{"baseUrl": "http://box.lan:11434"}), "http://box.lan:11434/api/chat"},
		"https":            {tests.Account(map[string]string{"baseUrl": "https://ollama.example"}), "https://ollama.example/api/chat"},
	} {
		got, err := ollamaAddress(executor.Request{Account: tc.acct})
		if err != nil || got != tc.want {
			t.Errorf("%s: %q %v, want %q", name, got, err, tc.want)
		}
	}
}

func TestOllamaRefusesAnAddressItCannotUse(t *testing.T) {
	for name, raw := range map[string]string{
		"a scheme that is not http":  "ftp://box.lan:11434",
		"a query string":             "http://box.lan:11434?x=1",
		"a fragment":                 "http://box.lan:11434#x",
		"credentials in the address": "http://user:pw@box.lan:11434",
		"no host":                    "http://",
		"a relative path":            "/var/run/ollama",
	} {
		if _, err := ollamaAddress(executor.Request{Account: tests.Account(map[string]string{"baseUrl": raw})}); !errors.Is(err, executor.ErrBadAccount) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOllamaAClientLeftTheKeyFlowsAndNoKeySendsNoBearer(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{"message":{"role":"assistant","content":"hi"},"done":true}`})
	o := &OllamaLocal{Default: &executor.Default{Client: up.Server.Client()}}

	req := executor.Request{URL: up.URL, Key: "k", Body: []byte(`{}`), Account: tests.Account(map[string]string{"baseUrl": up.URL})}
	if _, err := o.Execute(context.Background(), req); err != nil {
		t.Fatalf("with a key: %v", err)
	}
	if got := up.Calls()[0].Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("with a key: authorization %q", got)
	}
	if got := up.Calls()[0].Path; got != "/api/chat" {
		t.Errorf("path %q", got)
	}

	up.Reset(tests.Reply{Body: `{}`})
	req.Key = ""
	resp, err := o.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("without a key: %v", err)
	}
	resp.Body.Close()
	if got := up.Calls()[0].Header.Get("Authorization"); got != "" {
		t.Errorf("without a key: authorization %q, want none", got)
	}
}
