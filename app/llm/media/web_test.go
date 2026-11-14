package media

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
)

// plain is a client with no cap of its own.
type plain struct{ body []byte }

func (p plain) Do(context.Context, string, string, map[string]string, []byte) (int, http.Header, []byte, error) {
	return 200, http.Header{}, p.body, nil
}

// capping is a client that bounds its own read.
type capping struct{ limit int64 }

func (c *capping) Do(context.Context, string, string, map[string]string, []byte) (int, http.Header, []byte, error) {
	return 0, nil, nil, errors.New("the capped path was not used")
}

func (c *capping) DoCapped(_ context.Context, _, _ string, _ map[string]string, _ []byte, limit int64) (int, http.Header, []byte, error) {
	c.limit = limit
	return 200, http.Header{}, []byte("ok"), nil
}

func TestACappedCallUsesTheClientsOwnCapWhenItHasOne(t *testing.T) {
	c := &capping{}
	if _, _, _, err := Capped(context.Background(), c, "GET", "https://x.test", nil, nil, 1234); err != nil || c.limit != 1234 {
		t.Fatalf("limit %d, err %v", c.limit, err)
	}
}

func TestACappedCallRefusesAnAnswerPastTheLimit(t *testing.T) {
	_, _, _, err := Capped(context.Background(), plain{make([]byte, 11)}, "GET", "https://x.test", nil, nil, 10)
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a provider error", err)
	}
	if _, _, body, err := Capped(context.Background(), plain{make([]byte, 10)}, "GET", "https://x.test", nil, nil, 10); err != nil || len(body) != 10 {
		t.Fatalf("an answer at the limit: %d, %v", len(body), err)
	}
}

func TestAProviderAddressMustBeHTTPSWithAHostAndNoCredential(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://cdn.example/a.png":     true,
		"http://cdn.example/a.png":      false,
		"https://user:pw@cdn.example/a": false,
		"https:///nohost":               false,
		"ftp://cdn.example/a":           false,
		"not a url":                     false,
		"":                              false,
	} {
		if got := HTTPSOnly(raw) == nil; got != ok {
			t.Errorf("HTTPSOnly(%q) = %v, want %v", raw, got, ok)
		}
	}
}

func TestAWebPromptIsBoundedAndMakesOneItem(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		ok   bool
	}{
		"a prompt":    {`{"prompt":"a fox"}`, true},
		"n of one":    {`{"prompt":"a fox","n":1}`, true},
		"no prompt":   {`{}`, false},
		"a blank one": {`{"prompt":"   "}`, false},
		"too long":    {`{"prompt":"` + strings.Repeat("a", MaxWebPrompt+1) + `"}`, false},
		"two items":   {`{"prompt":"a fox","n":2}`, false},
		"zero items":  {`{"prompt":"a fox","n":0}`, false},
	} {
		_, err := WebPrompt([]byte(tc.body))
		if (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, tc.ok)
		}
		if err != nil {
			var bad *BadRequest
			if !errors.As(err, &bad) {
				t.Errorf("%s: %v is not the request's fault", name, err)
			}
		}
	}
}

func TestPollEveryStopsOnDoneErrorTimeoutOrCancel(t *testing.T) {
	never := errors.New("timed out")
	if err := PollEvery(context.Background(), time.Millisecond, time.Second, func(context.Context) (bool, error) { return true, nil }, never); err != nil {
		t.Fatalf("done: %v", err)
	}
	boom := errors.New("boom")
	if err := PollEvery(context.Background(), time.Millisecond, time.Second, func(context.Context) (bool, error) { return false, boom }, never); !errors.Is(err, boom) {
		t.Fatalf("error: %v", err)
	}
	if err := PollEvery(context.Background(), time.Millisecond, 20*time.Millisecond, func(context.Context) (bool, error) { return false, nil }, never); !errors.Is(err, never) {
		t.Fatalf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PollEvery(ctx, time.Hour, time.Hour, func(context.Context) (bool, error) { return false, nil }, never); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	calls := 0
	if err := PollEvery(context.Background(), time.Millisecond, time.Second, func(context.Context) (bool, error) { calls++; return calls == 3, nil }, never); err != nil || calls != 3 {
		t.Fatalf("calls %d, err %v", calls, err)
	}
}

func TestSleepEndsWithTheContext(t *testing.T) {
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("a short sleep: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("a cancelled sleep: %v after %s", err, time.Since(started))
	}
}

func TestTheServiceAddressNeverEndsInASlash(t *testing.T) {
	for name, r := range map[string]Request{
		"the config's":  {Config: &upstream.MediaConfig{BaseURL: "https://api.test/v1/"}},
		"the account's": {Account: map[string]string{"baseUrl": "https://acct.test///"}},
		"neither":       {},
	} {
		if got := BaseURL(r); strings.HasSuffix(got, "/") {
			t.Errorf("%s: %q ends in a slash", name, got)
		}
	}
	if got := BaseURL(Request{Config: &upstream.MediaConfig{BaseURL: "https://api.test/v1/"}, Account: map[string]string{"baseUrl": "https://acct.test"}}); got != "https://api.test/v1" {
		t.Errorf("the config wins over the account: %q", got)
	}
}
