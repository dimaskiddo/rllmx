package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

func run(t *testing.T, d *Default, req Request) (Response, error) {
	t.Helper()
	if d.Client == nil {
		d.Client = http.DefaultClient
	}
	return d.Execute(context.Background(), req)
}

func TestExecuteSendsTheKeyUnderEachScheme(t *testing.T) {
	cases := map[string]struct {
		auth        *upstream.Auth
		header      string
		want        string
		queryParam  string
		staticName  string
		staticValue string
	}{
		"bearer": {auth: &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization"}, header: "Authorization", want: "Bearer " + llm.Secret},
		"raw":    {auth: &upstream.Auth{Scheme: upstream.SchemeRaw, Header: "x-api-key", Headers: map[string]string{"anthropic-version": "2023-06-01"}}, header: "x-api-key", want: llm.Secret, staticName: "anthropic-version", staticValue: "2023-06-01"},
		"query":  {auth: &upstream.Auth{Scheme: upstream.SchemeQuery, Header: "key"}, queryParam: "key"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
			resp, err := run(t, &Default{}, Request{URL: up.URL + "/v1/x", Body: []byte(`{"a":1}`), Key: llm.Secret, Auth: c.auth})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			call := up.Calls()[0]
			if c.header != "" && call.Header.Get(c.header) != c.want {
				t.Fatalf("%s header = %q", c.header, call.Header.Get(c.header))
			}
			if c.queryParam != "" && !strings.Contains(call.Query, c.queryParam+"="+llm.Secret) {
				t.Fatalf("query = %q", call.Query)
			}
			if c.staticName != "" && call.Header.Get(c.staticName) != c.staticValue {
				t.Fatalf("static header = %q", call.Header.Get(c.staticName))
			}
			if call.Header.Get("Content-Type") != "application/json" || string(call.Body) != `{"a":1}` {
				t.Fatalf("%v %q", call.Header, call.Body)
			}
		})
	}
}

func TestOverridesComeLastButNeverReplaceTheCredential(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	resp, err := run(t, &Default{}, Request{
		URL: up.URL, Key: llm.Secret, Auth: llm.Bearer(), Stream: true,
		Overrides: map[string]string{
			"X-Extra": "yes", "Authorization": "Bearer stolen", "Cookie": "a=b", "Content-Type": "text/plain",
			"Host": "evil.example", "Accept": "application/x-custom", "X-Bad": "a\nb",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := up.Calls()[0].Header
	if h.Get("Authorization") != "Bearer "+llm.Secret || h.Get("Cookie") != "" || h.Get("Content-Type") != "application/json" {
		t.Fatalf("a reserved header was overridden: %v", h)
	}
	if h.Get("X-Extra") != "yes" || h.Get("X-Bad") != "" {
		t.Fatalf("overrides: %v", h)
	}
	if h.Get("Accept") != "application/x-custom" {
		t.Fatalf("an override must win over Accept: %q", h.Get("Accept"))
	}
}

func TestStreamAddsTheAcceptHeader(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	for _, stream := range []bool{true, false} {
		resp, err := run(t, &Default{}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer(), Stream: stream})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	calls := up.Calls()
	if calls[0].Header.Get("Accept") != "text/event-stream" || calls[1].Header.Get("Accept") != "" {
		t.Fatalf("%q %q", calls[0].Header.Get("Accept"), calls[1].Header.Get("Accept"))
	}
}

func TestRetryTableByStatus(t *testing.T) {
	cases := map[int]struct {
		calls int
		wait  time.Duration
	}{
		502: {4, 3 * time.Second}, 503: {4, 2 * time.Second}, 504: {3, 3 * time.Second}, 429: {1, 0}, 500: {1, 0}, 400: {1, 0},
	}
	for status, c := range cases {
		t.Run(http.StatusText(status), func(t *testing.T) {
			up := tests.NewUpstream(t, tests.Reply{Status: status, Body: `{"error":{"message":"x"}}`})
			var waits []time.Duration
			resp, err := run(t, &Default{Sleep: tests.InstantSleep(&waits)}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer()})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.Status != status || len(up.Calls()) != c.calls {
				t.Fatalf("status %d after %d calls", resp.Status, len(up.Calls()))
			}
			for _, w := range waits {
				if w != c.wait {
					t.Fatalf("waited %v, want %v", w, c.wait)
				}
			}
		})
	}
}

func TestARetryThatSucceedsReturnsTheGoodAnswer(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 503, Body: "busy"}, tests.Reply{Body: `{"ok":true}`})
	var waits []time.Duration
	resp, err := run(t, &Default{Sleep: tests.InstantSleep(&waits)}, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer()})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Status != 200 || string(body) != `{"ok":true}` || len(up.Calls()) != 2 {
		t.Fatalf("%d %q after %d calls", resp.Status, body, len(up.Calls()))
	}
}

func TestARuleWithoutADelayWaitsTwoSeconds(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 500}, tests.Reply{})
	var waits []time.Duration
	d := &Default{Sleep: tests.InstantSleep(&waits), Retry: map[int]Rule{500: {Attempts: 1}}}
	resp, err := run(t, d, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer()})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(waits) != 1 || waits[0] != 2*time.Second {
		t.Fatalf("waits %v", waits)
	}
}

type countingBody struct {
	io.Reader
	closed *bool
}

func (c countingBody) Close() error { *c.closed = true; return nil }

type scriptedTransport struct {
	responses []*http.Response
	calls     int
}

func (s *scriptedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r := s.responses[min(s.calls, len(s.responses)-1)]
	s.calls++
	return r, nil
}

func TestTheBodyOfARetriedAttemptIsClosed(t *testing.T) {
	var closed bool
	first := &http.Response{StatusCode: 503, Header: http.Header{}, Body: countingBody{strings.NewReader("busy"), &closed}}
	second := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}
	var waits []time.Duration
	d := &Default{Client: &http.Client{Transport: &scriptedTransport{responses: []*http.Response{first, second}}}, Sleep: tests.InstantSleep(&waits)}
	resp, err := d.Execute(context.Background(), Request{URL: "http://upstream.invalid/x", Key: llm.Secret, Auth: llm.Bearer()})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !closed {
		t.Fatal("the body of the retried attempt was left open")
	}
}

func TestRetryStopsWhenTheContextEnds(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 503})
	ctx, cancel := context.WithCancel(context.Background())
	d := &Default{Client: http.DefaultClient, Sleep: func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}}
	_, err := d.Execute(ctx, Request{URL: up.URL, Key: llm.Secret, Auth: llm.Bearer()})
	if !errors.Is(err, context.Canceled) || len(up.Calls()) != 1 {
		t.Fatalf("err %v after %d calls", err, len(up.Calls()))
	}
}

func TestAConnectionFailureIsAFixedErrorWithNoAddress(t *testing.T) {
	var waits []time.Duration
	_, err := run(t, &Default{Sleep: tests.InstantSleep(&waits)}, Request{URL: "http://127.0.0.1:1/x", Key: llm.Secret, Auth: llm.Bearer()})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), llm.Secret) {
		t.Fatalf("the error carries the address or the key: %v", err)
	}
	if len(waits) != 3 {
		t.Fatalf("a network error must follow the 502 rule, waited %d times", len(waits))
	}
}

func TestNoKeyIsRefusedBeforeAnythingIsSent(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{})
	_, err := run(t, &Default{}, Request{URL: up.URL, Auth: llm.Bearer()})
	if !errors.Is(err, ErrNoCredential) || len(up.Calls()) != 0 {
		t.Fatalf("err %v after %d calls", err, len(up.Calls()))
	}
}

func TestURLs(t *testing.T) {
	reg := upstream.Entry{Transport: upstream.Transport{BaseURL: "https://api.example/v1/chat/completions"}}
	if got := RegistryURL(reg, upstream.FormatOpenAI, "m", false); got != "https://api.example/v1/chat/completions" {
		t.Fatal(got)
	}
	gem := upstream.Entry{Transport: upstream.Transport{BaseURL: "https://g.example/v1beta/models/"}}
	if got := RegistryURL(gem, upstream.FormatGemini, "gemini-x", true); got != "https://g.example/v1beta/models/gemini-x:streamGenerateContent?alt=sse" {
		t.Fatal(got)
	}
	if got := RegistryURL(gem, upstream.FormatGemini, "gemini-x", false); got != "https://g.example/v1beta/models/gemini-x:generateContent" {
		t.Fatal(got)
	}
	if got := RegistryURL(gem, upstream.FormatGemini, "a/b?c", false); strings.Contains(got, "a/b") || strings.Contains(got, "?c") {
		t.Fatalf("a model id changed the path: %s", got)
	}
	for _, c := range []struct{ typ, base, api, want string }{
		{upstream.NodeTypeOpenAI, "https://n.example/v1/", "chat", "https://n.example/v1/chat/completions"},
		{upstream.NodeTypeOpenAI, "https://n.example/v1", "responses", "https://n.example/v1/responses"},
		{upstream.NodeTypeAnthropic, "https://n.example/v1", "", "https://n.example/v1/messages"},
		{upstream.NodeTypeEmbedding, "https://n.example/v1", "", "https://n.example/v1/embeddings"},
	} {
		if got := NodeURL(c.typ, c.base, c.api); got != c.want {
			t.Errorf("%s: %s, want %s", c.typ, got, c.want)
		}
	}
}

func TestParseErrorPicksTheMessageAndStripsMarkup(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"openai":    {`{"error":{"message":"Rate limit reached","type":"x"}}`, "Rate limit reached"},
		"flat":      {`{"message":"bad thing"}`, "bad thing"},
		"string":    {`{"error":"quota gone"}`, "quota gone"},
		"object":    {`{"error":{"code":7}}`, `{"code":7}`},
		"html":      {"<html><head><title>502</title></head><body><h1>Bad Gateway</h1></body></html>", "502 Bad Gateway"},
		"escaped":   {`{"message":"&lt;script&gt;alert(1)&lt;/script&gt; down"}`, "alert(1) down"},
		"control":   {"{\"message\":\"a\\u0007b\\u0085c\"}", "abc"},
		"plain":     {"upstream exploded", "upstream exploded"},
		"empty":     {"", ""},
		"not json":  {"{", "{"},
		"long text": {`{"message":"` + strings.Repeat("x", 5000) + `"}`, strings.Repeat("x", maxMessage)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := ParseError(500, []byte(c.in))
			if got.Message != c.want || got.Status != 500 {
				t.Fatalf("%q, want %q", got.Message, c.want)
			}
		})
	}
}

func TestParseErrorReadsAProviderReset(t *testing.T) {
	at := ParseError(429, []byte(`{"error":{"resets_at":4102444800}}`)).ResetAt
	if at.Unix() != 4102444800 {
		t.Fatalf("%v", at)
	}
	in := ParseError(429, []byte(`{"error":{"resets_in_seconds":90}}`)).ResetAt
	if d := time.Until(in); d < 80*time.Second || d > 91*time.Second {
		t.Fatalf("%v", d)
	}
	if !ParseError(429, []byte(`{"error":{"message":"x"}}`)).ResetAt.IsZero() {
		t.Fatal("a reset appeared from nowhere")
	}
}

func TestARetryAfterHeaderBecomesTheResetTime(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("Retry-After", "120")
	if got := RetryAt(h, now); got.Sub(now) != 120*time.Second {
		t.Fatalf("%v", got)
	}
	h.Set("Retry-After", now.Add(90*time.Second).UTC().Format(http.TimeFormat))
	if got := RetryAt(h, now); got.Sub(now) != 90*time.Second {
		t.Fatalf("%v", got)
	}
	h = http.Header{}
	h.Set("X-RateLimit-Reset", strconv.FormatInt(now.Unix()+45, 10))
	if got := RetryAt(h, now); got.Sub(now) != 45*time.Second {
		t.Fatalf("%v", got)
	}
	h = http.Header{}
	h.Set("X-RateLimit-Reset-After", "30")
	if got := RetryAt(h, now); got.Sub(now) != 30*time.Second {
		t.Fatalf("%v", got)
	}
	if !RetryAt(http.Header{}, now).IsZero() {
		t.Fatal("a hint appeared from nowhere")
	}
	body := io.NopCloser(strings.NewReader(`{"error":{"resets_at":4102444800}}`))
	stale := http.Header{}
	stale.Set("Retry-After", "120")
	if e := ReadErrorHeaders(429, stale, body); e.ResetAt.Unix() != 4102444800 {
		t.Fatalf("the body's own reset lost to the header: %v", e.ResetAt)
	}
}

func TestReadErrorIsBounded(t *testing.T) {
	big := strings.NewReader(`{"message":"` + strings.Repeat("y", MaxErrorBody*2) + `"}`)
	e := ReadError(500, big)
	if len(e.Message) > maxMessage {
		t.Fatalf("message of %d bytes", len(e.Message))
	}
}

func FuzzParseError(f *testing.F) {
	for _, s := range []string{`{"error":{"message":"x"}}`, `<html>`, ``, `{"error":`, "\x00\xff", `{"error":{"resets_at":-1}}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		e := ParseError(500, in)
		if len(e.Message) > maxMessage || strings.ContainsAny(e.Message, "<>") && strings.Contains(e.Message, "</") {
			t.Fatalf("message %q", e.Message)
		}
	})
}
