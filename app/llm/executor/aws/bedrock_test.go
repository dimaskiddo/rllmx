package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/app/protocol/eventstream"
	"rllmx/tests"
)

const bedrockKeyID = "AKIDEXAMPLE"

func bedrockAcct(kv map[string]string) func(string) string {
	base := map[string]string{"accessKeyId": bedrockKeyID, "region": "us-east-1"}
	for k, v := range kv {
		base[k] = v
	}
	return tests.Account(base)
}

// bedrockRedirect sends the signed address to the fake server, so the signature
// is computed for the real host and the test still sees the request.
func bedrockRedirect(t *testing.T, up *tests.Upstream) *http.Client {
	t.Helper()
	return &http.Client{Transport: tests.Redirect(func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://")
	})}
}

func TestBedrockAddress(t *testing.T) {
	for name, tc := range map[string]struct {
		region, model string
		stream        bool
		want          string
	}{
		"invoke": {"us-east-1", "us.anthropic.claude-opus-5", false,
			"https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-opus-5/invoke"},
		"stream": {"eu-west-1", "us.xai.grok-4.6", true,
			"https://bedrock-runtime.eu-west-1.amazonaws.com/model/us.xai.grok-4.6/invoke-with-response-stream"},
		"versioned id": {"us-east-1", "us.anthropic.claude-opus-4-5-20251101-v1:0", false,
			"https://bedrock-runtime.us-east-1.amazonaws.com/model/us.anthropic.claude-opus-4-5-20251101-v1%3A0/invoke"},
	} {
		if got := bedrockAddress(tc.region, tc.model, tc.stream); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
	}
}

func TestABedrockAnthropicBodyLosesTheModelAndGainsTheVersion(t *testing.T) {
	b := &Bedrock{}
	body := []byte(`{"model":"m","stream":true,"max_tokens":8,"anthropic_version":"2023-06-01"}`)
	got := string(b.body(executor.Request{Body: body}))
	for _, gone := range []string{`"model"`, `"stream"`} {
		if strings.Contains(got, gone) {
			t.Errorf("%s survived: %s", gone, got)
		}
	}
	if !strings.Contains(got, `"anthropic_version":"bedrock-2023-05-31"`) {
		t.Errorf("the client's version won: %s", got)
	}
	if !strings.Contains(got, `"max_tokens":8`) {
		t.Errorf("the rest was dropped: %s", got)
	}
}

func TestABedrockChatBodyOnlyLosesTheModelAndTheStream(t *testing.T) {
	b := &Bedrock{XAI: true}
	got := string(b.body(executor.Request{Body: []byte(`{"model":"m","stream":true,"messages":[]}`)}))
	if strings.Contains(got, `"model"`) || strings.Contains(got, `"stream"`) || strings.Contains(got, "anthropic_version") {
		t.Errorf("body %s", got)
	}
	if !strings.Contains(got, `"messages":[]`) {
		t.Errorf("the messages were dropped: %s", got)
	}
}

func TestAModelFromTheWrongFamilyIsRefusedBeforeItIsSent(t *testing.T) {
	for name, tc := range map[string]struct {
		xai   bool
		model string
	}{
		"bedrock takes no grok":        {false, "us.xai.grok-4.6"},
		"bedrock-xai takes no claude":  {true, "us.anthropic.claude-opus-5"},
		"a name with no family at all": {false, "claude-opus-5"},
	} {
		up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
		b := &Bedrock{Default: &executor.Default{Client: http.DefaultClient}, XAI: tc.xai}
		_, err := b.Execute(context.Background(), executor.Request{URL: up.URL, Key: "secret", Model: tc.model, Account: bedrockAcct(nil)})
		if !errors.Is(err, executor.ErrBadAccount) || len(up.Calls()) != 0 {
			t.Errorf("%s: %v, %d calls", name, err, len(up.Calls()))
		}
	}
}

func TestABadOrMissingBedrockCredentialSendsNothing(t *testing.T) {
	cases := map[string]struct {
		key  string
		acct func(string) string
		want error
	}{
		"no secret":            {"", bedrockAcct(nil), executor.ErrNoCredential},
		"no key id":            {"secret", tests.Account(map[string]string{"region": "us-east-1"}), executor.ErrBadAccount},
		"no account":           {"secret", nil, executor.ErrBadAccount},
		"region with a path":   {"secret", bedrockAcct(map[string]string{"region": "evil.com/x"}), executor.ErrBadAccount},
		"ASIA without a token": {"secret", bedrockAcct(map[string]string{"accessKeyId": "ASIAXXXX"}), executor.ErrBadAccount},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
			b := &Bedrock{Default: &executor.Default{Client: http.DefaultClient}}
			_, err := b.Execute(context.Background(), executor.Request{URL: up.URL, Key: tc.key, Model: "us.anthropic.m", Account: tc.acct})
			if !errors.Is(err, tc.want) || len(up.Calls()) != 0 {
				t.Errorf("%v, %d calls", err, len(up.Calls()))
			}
		})
	}
}

func TestABedrockCallIsSignedAndCarriesTheAccountKeyID(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	b := &Bedrock{Default: &executor.Default{Client: bedrockRedirect(t, up)}}
	resp, err := b.Execute(context.Background(), executor.Request{
		Key: "SECRET", Model: "us.anthropic.claude-opus-5", Body: []byte(`{"model":"x"}`), Account: bedrockAcct(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := up.Calls()[0]
	auth := c.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/") {
		t.Fatalf("authorization %q", auth)
	}
	if c.Header.Get("X-Amz-Date") == "" || c.Header.Get("Accept") != "application/json" {
		t.Errorf("headers %v", c.Header)
	}
	if strings.Contains(string(c.Body), "SECRET") || strings.Contains(auth, "SECRET") {
		t.Error("the secret reached the request")
	}
}

func TestAStreamedBedrockCallAsksForTheEventStream(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	b := &Bedrock{Default: &executor.Default{Client: bedrockRedirect(t, up)}}
	resp, err := b.Execute(context.Background(), executor.Request{
		Key: "SECRET", Model: "us.anthropic.m", Stream: true, Body: []byte(`{}`), Account: bedrockAcct(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := up.Calls()[0].Header.Get("Accept"); got != "application/vnd.amazon.eventstream" {
		t.Errorf("accept %q", got)
	}
}

func TestASessionTokenIsSentAndSigned(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	b := &Bedrock{Default: &executor.Default{Client: bedrockRedirect(t, up)}}
	resp, err := b.Execute(context.Background(), executor.Request{
		Key: "SECRET", Model: "us.anthropic.m", Body: []byte(`{}`),
		Account: bedrockAcct(map[string]string{"accessKeyId": "ASIAEXAMPLE", "sessionToken": "TOKEN"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	c := up.Calls()[0]
	if c.Header.Get("X-Amz-Security-Token") != "TOKEN" {
		t.Errorf("token header %q", c.Header.Get("X-Amz-Security-Token"))
	}
	if !strings.Contains(c.Header.Get("Authorization"), "x-amz-security-token") {
		t.Errorf("the token was not signed: %q", c.Header.Get("Authorization"))
	}
}

func TestABedrockRedirectIsNotFollowed(t *testing.T) {
	// The signature and the session token must never be replayed at whatever
	// origin a redirect names.
	target := tests.NewUpstream(t, tests.Reply{Body: `{"leaked":true}`})
	up := tests.NewUpstream(t, tests.Reply{Status: http.StatusTemporaryRedirect, Header: map[string]string{"Location": target.URL}})
	b := &Bedrock{Default: &executor.Default{Client: bedrockRedirect(t, up)}}
	resp, err := b.Execute(context.Background(), executor.Request{Key: "SECRET", Model: "us.anthropic.m", Body: []byte(`{}`), Account: bedrockAcct(nil)})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Status != http.StatusTemporaryRedirect {
		t.Errorf("status %d, want the redirect returned", resp.Status)
	}
	if len(target.Calls()) != 0 {
		t.Error("the redirect was followed")
	}
}

func TestTheEventStreamBodyIsReadAsEvents(t *testing.T) {
	inner := `{"delta":{"text":"hi"},"type":"content_block_delta"}`
	body := eventstream.Encode(map[string]string{":event-type": "chunk", ":message-type": "event"},
		[]byte(`{"bytes":"`+base64.StdEncoding.EncodeToString([]byte(inner))+`"}`))
	up := tests.NewUpstream(t, tests.Reply{Body: string(body), ContentType: "application/vnd.amazon.eventstream"})
	b := &Bedrock{Default: &executor.Default{Client: bedrockRedirect(t, up)}}
	resp, err := b.Execute(context.Background(), executor.Request{Key: "SECRET", Model: "us.anthropic.m", Stream: true, Body: []byte(`{}`), Account: bedrockAcct(nil)})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "application/vnd.amazon.eventstream" {
		t.Errorf("content type %q, want the upstream's own: the pipeline reads the framing", got)
	}
}
