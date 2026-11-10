package executor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

func TestAProvidersRetryRuleBeatsTheBuiltInTable(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 429}, tests.Reply{Status: 429}, tests.Reply{Status: 429}, tests.Reply{Body: `{}`})
	e := upstream.Entry{Transport: upstream.Transport{Retry: map[int]upstream.Retry{429: {Attempts: 2, DelayMs: 250}}}}
	var waits []time.Duration
	resp, err := run(t, &Default{Sleep: tests.InstantSleep(&waits)}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer(), Entry: e})
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
	resp, err := run(t, &Default{Sleep: tests.InstantSleep(&waits)}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer()})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Status != 429 || len(up.Calls()) != 1 {
		t.Fatalf("a 429 was retried in place: %d calls", len(up.Calls()))
	}
	up = tests.NewUpstream(t, tests.Reply{Status: 503}, tests.Reply{Body: `{}`})
	e := upstream.Entry{Transport: upstream.Transport{Retry: map[int]upstream.Retry{429: {Attempts: 2}}}}
	resp, err = run(t, &Default{Sleep: tests.InstantSleep(&waits)}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer(), Entry: e})
	if err != nil || resp.Status != 200 {
		t.Fatalf("the 503 rule was lost: %v %d", err, resp.Status)
	}
	resp.Body.Close()
}

func TestTransportHeadersAreSentAndCannotReplaceTheKey(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	e := upstream.Entry{Transport: upstream.Transport{Headers: map[string]string{"X-Title": "t", "Authorization": "Bearer stolen"}}}
	resp, err := run(t, &Default{}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer(), Entry: e})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := up.Calls()[0].Header
	if h.Get("X-Title") != "t" || h.Get("Authorization") != "Bearer "+llm.Secret {
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
	resp, err := run(t, &Default{}, Request{URL: up.URL + addr, Key: llm.Secret, Auth: llm.Bearer(), Account: tests.Account(map[string]string{"accountId": "abc123"})})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := up.Calls()[0].Path; got != "/client/v4/accounts/abc123/ai/v1/chat/completions" {
		t.Fatalf("path %s", got)
	}
	for name, id := range map[string]string{"missing": "", "slash": "a/b", "dots": "..", "query": "a?b", "space": "a b", "newline": "a\nb", "long": strings.Repeat("a", 129)} {
		before := len(up.Calls())
		_, err := run(t, &Default{}, Request{URL: up.URL + addr, Key: llm.Secret, Auth: llm.Bearer(), Account: tests.Account(map[string]string{"accountId": id})})
		if !errors.Is(err, ErrBadAccount) || len(up.Calls()) != before {
			t.Errorf("%s: err %v, %d calls sent", name, err, len(up.Calls())-before)
		}
	}
	if _, err := run(t, &Default{}, Request{URL: up.URL + addr, Key: llm.Secret, Auth: llm.Bearer()}); !errors.Is(err, ErrBadAccount) {
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
		_, err := run(t, &Default{}, Request{URL: addr, Key: llm.Secret, Auth: llm.Bearer(), Account: tests.Account(map[string]string{"baseUrl": base})})
		if !errors.Is(err, ErrBadAccount) || len(up.Calls()) != before {
			t.Errorf("%s: err %v, %d calls sent", name, err, len(up.Calls())-before)
		}
	}
	if _, err := run(t, &Default{}, Request{URL: addr, Key: llm.Secret, Auth: llm.Bearer()}); !errors.Is(err, ErrBadAccount) {
		t.Fatalf("no account reader: %v", err)
	}
	got, err := fillAccount(Request{URL: addr, Account: tests.Account(map[string]string{"baseUrl": "https://git.example:8443"})})
	if err != nil || got != "https://git.example:8443/api/v4/chat/completions" {
		t.Fatalf("%q %v", got, err)
	}
}
