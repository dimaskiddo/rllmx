package executor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/cosy"
	"rllmx/app/upstream"
	"rllmx/tests"
)

// qoderTestKey is a throwaway pair, made here. The vendor's own key is data and
// is never used by a test.
func qoderTestKey(t *testing.T) *rsa.PublicKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return &key.PublicKey
}

func qoderReq(model string) Request {
	return Request{Key: "dt-1", Model: model, Session: "conv-1",
		Body: []byte(`{"model":"` + model + `","messages":[
			{"role":"system","content":"be brief"},
			{"role":"user","content":"list the files"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"ls","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"c1","content":"a.txt"}],
			"tools":[{"type":"function","function":{"name":"ls","parameters":{"type":"object"}}}],"max_tokens":64}`),
		Account: acct(map[string]string{"userId": "u1", "machineId": "m-1"}),
		Entry:   upstream.Entry{Executor: upstream.ExecutorQoder},
	}
}

func TestTheQoderBodyCarriesTheModelConfiguration(t *testing.T) {
	out, err := qoderBody(qoderReq("auto"), "auto", `{"key":"auto","source":"system","is_reasoning":true,"max_output_tokens":128}`)
	if err != nil {
		t.Fatalf("%v", err)
	}
	g := gjson.ParseBytes(out)
	if got := g.Get("system").String(); got != "be brief" {
		t.Errorf("system %q", got)
	}
	if got := g.Get("messages.#").Int(); got != 3 {
		t.Errorf("%d turns, want the system turn lifted: %s", got, g.Get("messages").Raw)
	}
	if got := g.Get("messages.0.role").String(); got != "user" {
		t.Errorf("first turn %q", got)
	}
	if got := g.Get("parameters.max_tokens").Int(); got != 64 {
		t.Errorf("max_tokens %d, want the client's 64", got)
	}
	if got := g.Get("model_config.key").String(); got != "auto" {
		t.Errorf("model_config %s", g.Get("model_config").Raw)
	}
	if got := g.Get("chat_context.extra.modelConfig.is_reasoning").Bool(); !got {
		t.Error("the reasoning flag was not carried")
	}
	if got := g.Get("chat_context.text").String(); got != "list the files" {
		t.Errorf("the last user turn is %q", got)
	}
	if got := g.Get("business.name").String(); got != "list the files" {
		t.Errorf("business name %q", got)
	}
	if !g.Get("stream").Bool() {
		t.Error("the request was not forced to stream")
	}
	if got := g.Get("session_id").String(); len(got) != 16 {
		t.Errorf("session id %q", got)
	}
}

func TestTheQoderRecordIsStableAndTheRequestIsNot(t *testing.T) {
	first, _ := qoderBody(qoderReq("auto"), "auto", `{"key":"auto"}`)
	second, _ := qoderBody(qoderReq("auto"), "auto", `{"key":"auto"}`)
	a, b := gjson.ParseBytes(first), gjson.ParseBytes(second)
	if a.Get("request_set_id").String() != b.Get("request_set_id").String() {
		t.Error("one prompt got two record ids")
	}
	if a.Get("session_id").String() != b.Get("session_id").String() {
		t.Error("one conversation got two session ids")
	}
	if a.Get("request_id").String() == b.Get("request_id").String() {
		t.Error("two requests share one id, which the gateway refuses")
	}
}

func TestTheQoderContentFlattensAndStubsFiles(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		want    string
	}{
		"text":           {`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, `"a\nb"`},
		"an image":       {`[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]`, `[{"type":"text","text":"what is this"},{"type":"image_url","image_url":{"url":"https://x/a.png"}}]`},
		"a claude image": {`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]`, `[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAA"}}]`},
		"a file":         {`[{"type":"file","file":{"filename":"big.pdf"}}]`, `"[file omitted: big.pdf — the gateway reads documents through its own file API, not inlined bytes]"`},
	} {
		got := qoderContent(gjson.Parse(tc.content))
		if got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
	}
}

func TestTheQoderIdentityIsStablePerAccount(t *testing.T) {
	q := &Qoder{}
	derived := qoderReq("auto")
	derived.Account = acct(map[string]string{"userId": "u1"})
	first := q.identity(derived)
	if first.MachineID != q.identity(derived).MachineID || first.MachineID == "" {
		t.Fatalf("one account got %q then %q", first.MachineID, q.identity(derived).MachineID)
	}
	if len(first.MachineID) != 36 {
		t.Errorf("machine id %q is not uuid-shaped", first.MachineID)
	}
	other := qoderReq("auto")
	other.Account = acct(map[string]string{"userId": "u2"})
	if q.identity(other).MachineID == first.MachineID {
		t.Error("two accounts share a machine id")
	}
	// A stored machine id always wins over the derived one.
	if got := qoderMachineID("m-9", "u1"); got != "m-9" {
		t.Errorf("stored machine %q", got)
	}
}

func TestTheQoderHostFollowsTheSiteAndTheToken(t *testing.T) {
	intl := &Qoder{}
	cn := &Qoder{CN: true}
	for name, tc := range map[string]struct {
		q    *Qoder
		key  string
		want string
	}{
		"a device token": {intl, "dt-1", qoderIntlChat},
		"a job token":    {intl, "jt-1", qoderIntlChatAlt},
		"the other site": {cn, "dt-1", qoderCNChat},
		"a job on cn":    {cn, "jt-1", qoderCNChat},
	} {
		if got := tc.q.host(Request{Key: tc.key}); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

// qoderClient sends every address to the fake server, so the gateway's real
// hosts are never dialled.
func qoderClient(up *tests.Upstream) *http.Client {
	return &http.Client{Transport: roundTripper(func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://")
	})}
}

func TestQoderAnAccountWithoutAKeySendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	q := &Qoder{Default: &Default{Client: qoderClient(up)}, Public: qoderTestKey(t)}
	req := qoderReq("auto")
	req.Key = ""
	if _, err := q.Execute(context.Background(), req); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("%v", err)
	}
	if len(up.Calls()) != 0 {
		t.Fatal("a call went out")
	}
}

func TestQoderAModelTheCatalogLacksIsRefusedLocally(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{"chat":[{"key":"auto"}]}`})
	q := &Qoder{Default: &Default{Client: qoderClient(up)}, Public: qoderTestKey(t)}
	_, err := q.Execute(context.Background(), qoderReq("nowhere"))
	if !errors.Is(err, ErrNoModelConfig) {
		t.Fatalf("%v", err)
	}
	if len(up.Calls()) != 1 {
		t.Fatalf("%d calls, want only the catalog read", len(up.Calls()))
	}
}

func TestQoderSendsASignedEncodedBody(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Body: `{"chat":[{"key":"auto","source":"system"}]}`},
		tests.Reply{Body: "data: {\"statusCodeValue\":200}\n\n"},
	)
	q := &Qoder{Default: &Default{Client: qoderClient(up)}, Public: qoderTestKey(t)}
	req := qoderReq("auto")
	req.URL = up.URL
	if _, err := q.Execute(context.Background(), req); err != nil {
		t.Fatalf("%v", err)
	}
	call := up.Calls()[1]
	if got := call.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer COSY.") {
		t.Errorf("authorization %q", got)
	}
	if got := call.Header.Get("X-Model-Key"); got != "auto" {
		t.Errorf("model key %q", got)
	}
	if got := call.Header.Get("Accept-Encoding"); got != "identity" {
		t.Errorf("encoding %q", got)
	}
	// The body on the wire is the encoded form; decoding it gives the payload.
	plain, err := cosy.Decode(string(call.Body))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := gjson.GetBytes(plain, "request_set_id").String(); got == "" {
		t.Errorf("the decoded body has no record id: %s", plain)
	}
	if !strings.Contains(call.Query, "Encode=1") {
		t.Errorf("query %q", call.Query)
	}
}

func TestQoderAQueuedAnswerIsAskedAgain(t *testing.T) {
	queued := `data: {"statusCodeValue":200,"body":"{\"code\":\"10605\",\"message\":\"{\\\"isQueued\\\":true}\"}"}` + "\n\n"
	up := tests.NewUpstream(t,
		tests.Reply{Body: `{"chat":[{"key":"auto"}]}`},
		tests.Reply{Body: queued},
		tests.Reply{Body: "data: {\"statusCodeValue\":200}\n\n"},
	)
	var waits []time.Duration
	q := &Qoder{Default: &Default{Client: qoderClient(up)}, Public: qoderTestKey(t),
		Sleep: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }}
	req := qoderReq("auto")
	req.URL = up.URL
	resp, err := q.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("%v", err)
	}
	resp.Body.Close()
	if len(waits) != 1 || waits[0] < qoderQueueMinRetry {
		t.Fatalf("waits %v", waits)
	}
	if len(up.Calls()) != 3 {
		t.Fatalf("%d calls, want the queued one asked again", len(up.Calls()))
	}
}

func TestQoderAQueuedModelPastTheWaitIsAnsweredAsUnavailable(t *testing.T) {
	queued := `data: {"statusCodeValue":200,"body":"{\"code\":\"10605\"}"}` + "\n\n"
	up := tests.NewUpstream(t, tests.Reply{Body: `{"chat":[{"key":"auto"}]}`})
	defer up.Reset(tests.Reply{Body: queued})
	clock := time.Unix(1_700_000_000, 0)
	q := &Qoder{Default: &Default{Client: qoderClient(up)}, Public: qoderTestKey(t),
		Sleep: func(_ context.Context, d time.Duration) error { clock = clock.Add(d); return nil },
		Now:   func() time.Time { return clock }}
	up.Reset(tests.Reply{Body: `{"chat":[{"key":"auto"}]}`}, tests.Reply{Body: queued})
	req := qoderReq("auto")
	req.URL = up.URL
	start := clock
	resp, err := q.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("%v", err)
	}
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want the retryable refusal", resp.Status)
	}
	// The request is held for ten minutes, not for ever.
	if held := clock.Sub(start); held < 9*time.Minute || held > 11*time.Minute {
		t.Fatalf("the request was held %s, want about ten minutes", held)
	}
}

func TestQoderARefusalBeforeOutputBecomesItsStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		frame  string
		status int
	}{
		"a named status": {`data: {"statusCodeValue":429,"body":"slow down"}` + "\n\n", http.StatusTooManyRequests},
		"a billing code": {`data: {"statusCodeValue":500,"body":"{\"code\":\"112\",\"pricingUrl\":\"x\"}"}` + "\n\n", http.StatusForbidden},
		"an unnamed one": {`data: {"statusCodeValue":"302","body":"moved"}` + "\n\n", http.StatusBadGateway},
	} {
		up := tests.NewUpstream(t, tests.Reply{Body: `{"chat":[{"key":"auto"}]}`}, tests.Reply{Body: tc.frame})
		q := &Qoder{Default: &Default{Client: qoderClient(up)}, Public: qoderTestKey(t)}
		req := qoderReq("auto")
		req.URL = up.URL
		resp, err := q.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if resp.Status != tc.status {
			t.Errorf("%s: status %d, want %d", name, resp.Status, tc.status)
		}
	}
}
