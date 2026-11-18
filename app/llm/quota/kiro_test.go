package quota

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// kiroStub answers each Kiro attempt by method and host, and records the calls
// in order with the body and the headers of the POST.
type kiroStub struct {
	mu     sync.Mutex
	status map[string]int
	body   map[string]string
	calls  []string
	posted string
	header http.Header
}

const okKiro = `{"subscriptionInfo":{"subscriptionTitle":"Pro"},"nextDateReset":1795000000,"usageBreakdownList":[{"resourceType":"AGENTIC_REQUEST","currentUsageWithPrecision":12,"usageLimitWithPrecision":50}]}`

func (k *kiroStub) RoundTrip(r *http.Request) (*http.Response, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	key := r.Method + " " + r.URL.Host
	k.calls = append(k.calls, key)
	if r.Method == http.MethodPost {
		raw, _ := io.ReadAll(r.Body)
		k.posted, k.header = string(raw), r.Header.Clone()
	}
	status := k.status[key]
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(k.body[key])), Request: r,
	}, nil
}

func kiroReader(k *kiroStub, data string) (*Reader, Access) {
	conn := connOf("kiro", "oauth", data)
	return testReader(&stub{}, conn, "tok"), Access{Token: "tok", Conn: conn}
}

func readKiroWith(t *testing.T, k *kiroStub, data string) (Reading, error) {
	t.Helper()
	r, a := kiroReader(k, data)
	r.Client = &http.Client{Transport: k}
	entry, _ := r.entry("kiro")
	return readKiro(context.Background(), r, entry, a)
}

func TestKiroTriesTheNextAttemptWhenTheFirstIsNotOK(t *testing.T) {
	k := &kiroStub{
		status: map[string]int{"GET codewhisperer.us-east-1.amazonaws.com": 500, "POST codewhisperer.us-east-1.amazonaws.com": 502},
		body:   map[string]string{"GET q.us-east-1.amazonaws.com": okKiro},
	}
	out, err := readKiroWith(t, k, `{"providerSpecificData":{"authMethod":"builder-id"}}`)
	if err != nil || out.Plan != "Pro" || len(out.Quotas) == 0 {
		t.Fatalf("%v %+v", err, out)
	}
	want := "GET codewhisperer.us-east-1.amazonaws.com,POST codewhisperer.us-east-1.amazonaws.com,GET q.us-east-1.amazonaws.com"
	if got := strings.Join(k.calls, ","); got != want {
		t.Fatalf("attempts %s, want %s", got, want)
	}
}

func TestKiroStopsAtTheFirstAttemptThatAnswers(t *testing.T) {
	k := &kiroStub{body: map[string]string{"GET codewhisperer.us-east-1.amazonaws.com": okKiro}}
	if _, err := readKiroWith(t, k, `{"providerSpecificData":{"authMethod":"builder-id"}}`); err != nil {
		t.Fatal(err)
	}
	if len(k.calls) != 1 {
		t.Fatalf("%v", k.calls)
	}
}

func TestTheKiroPostCarriesTheTargetTheProfileAndTheTokenType(t *testing.T) {
	k := &kiroStub{status: map[string]int{"GET codewhisperer.us-east-1.amazonaws.com": 500}, body: map[string]string{"POST codewhisperer.us-east-1.amazonaws.com": okKiro}}
	data := `{"providerSpecificData":{"authMethod":"api_key","profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"}}`
	if _, err := readKiroWith(t, k, data); err != nil {
		t.Fatal(err)
	}
	if got := k.header.Get("X-Amz-Target"); got != "AmazonCodeWhispererService.GetUsageLimits" {
		t.Fatalf("target %q", got)
	}
	if k.header.Get("Content-Type") != "application/x-amz-json-1.0" || k.header.Get("TokenType") != "API_KEY" {
		t.Fatalf("headers %v", k.header)
	}
	for _, want := range []string{`"origin":"AI_EDITOR"`, `"resourceType":"AGENTIC_REQUEST"`, `"profileArn":"arn:aws:codewhisperer:us-east-1:1:profile/X"`} {
		if !strings.Contains(k.posted, want) {
			t.Errorf("body %s lacks %s", k.posted, want)
		}
	}
}

func TestAKiroRefusalOnEveryAttemptSaysTheTokenWasRefusedWithoutAnError(t *testing.T) {
	k := &kiroStub{status: map[string]int{
		"GET codewhisperer.us-east-1.amazonaws.com": 401, "POST codewhisperer.us-east-1.amazonaws.com": 403, "GET q.us-east-1.amazonaws.com": 403,
	}}
	out, err := readKiroWith(t, k, `{"providerSpecificData":{"authMethod":"builder-id"}}`)
	if err != nil {
		t.Fatalf("a refusal on a host the chat path does not use is a message, not an error: %v", err)
	}
	if !strings.Contains(out.Message, "Chat may still work") || len(out.Quotas) != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestAKiroFailureOnEveryAttemptIsASoftMessageWithoutTheBody(t *testing.T) {
	k := &kiroStub{
		status: map[string]int{"GET codewhisperer.us-east-1.amazonaws.com": 500, "POST codewhisperer.us-east-1.amazonaws.com": 500, "GET q.us-east-1.amazonaws.com": 503},
		body:   map[string]string{"GET q.us-east-1.amazonaws.com": "secret-token-in-an-error-page"},
	}
	out, err := readKiroWith(t, k, `{"providerSpecificData":{}}`)
	if err != nil || out.Message == "" || strings.Contains(out.Message, "secret-token") {
		t.Fatalf("%v %+v", err, out)
	}
	if errors.Is(err, ErrAuth) {
		t.Fatal("a server error became an auth error")
	}
}

func TestAnEuropeanAccountMovesOnlyTheQHost(t *testing.T) {
	k := &kiroStub{status: map[string]int{
		"GET codewhisperer.us-east-1.amazonaws.com": 500, "POST codewhisperer.us-east-1.amazonaws.com": 500,
	}, body: map[string]string{"GET q.eu-central-1.amazonaws.com": okKiro}}
	out, err := readKiroWith(t, k, `{"providerSpecificData":{"authMethod":"builder-id","region":"eu-central-1"}}`)
	if err != nil || out.Plan != "Pro" {
		t.Fatalf("%v %+v", err, out)
	}
	if got := k.calls[len(k.calls)-1]; got != "GET q.eu-central-1.amazonaws.com" {
		t.Fatalf("last call %s", got)
	}
}
