package executor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
	"rllmx/app/zed"
	"rllmx/tests"
)

// zedTokens is a scripted exchanger: each call returns the next token, and the
// third one fails.
type zedTokens struct {
	tokens []string
	forced []bool
	err    error
}

func (z *zedTokens) Token(_ context.Context, _ zed.Account, force bool) (string, error) {
	z.forced = append(z.forced, force)
	if z.err != nil {
		return "", z.err
	}
	if len(z.tokens) == 0 {
		return "", zed.ErrRefused
	}
	tok := z.tokens[0]
	z.tokens = z.tokens[1:]
	return tok, nil
}

func zedReq(up *tests.Upstream, format string) Request {
	return Request{
		URL: up.URL, Key: "AT", Model: "claude-x", Body: []byte(`{"messages":[{"role":"user","content":"hi"}],"model":"claude-x"}`),
		Session: "conv-1", Entry: upstream.Entry{Transport: upstream.Transport{Format: format},
			Executor: upstream.ExecutorZed},
		Account: acct(map[string]string{"userId": "U1", "organizationId": "org"}),
	}
}

func TestZedSendsTheEnvelopeWithAHostedToken(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	z := &Zed{Default: &Default{Client: up.Server.Client()}, Tokens: &zedTokens{tokens: []string{"LLM-1"}}}
	if _, err := z.Execute(context.Background(), zedReq(up, upstream.FormatClaude)); err != nil {
		t.Fatalf("%v", err)
	}
	call := up.Calls()[0]
	body := call.Body
	if got := gjson.GetBytes(body, "provider").String(); got != "anthropic" {
		t.Errorf("provider %q", got)
	}
	if got := gjson.GetBytes(body, "provider_request.messages.0.content").Raw; got == "" {
		t.Errorf("the provider's own request is missing: %s", body)
	}
	if got := call.Header.Get("Authorization"); got != "Bearer LLM-1" {
		t.Errorf("authorization %q", got)
	}
	if got := call.Header.Get("x-zed-version"); got == "" {
		t.Error("no version header")
	}
}

func TestZedMapsTheFormatToTheProviderName(t *testing.T) {
	for format, want := range map[string]string{
		upstream.FormatClaude:    "anthropic",
		upstream.FormatGemini:    "google",
		upstream.FormatResponses: "open_ai",
		upstream.FormatOpenAI:    "x_ai",
	} {
		up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
		z := &Zed{Default: &Default{Client: up.Server.Client()}, Tokens: &zedTokens{tokens: []string{"L"}}}
		if _, err := z.Execute(context.Background(), zedReq(up, format)); err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if got := gjson.GetBytes(up.Calls()[0].Body, "provider").String(); got != want {
			t.Errorf("%s: provider %q, want %q", format, got, want)
		}
	}
}

func TestZedDropsTheClientsSafetySettingsForGoogle(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	z := &Zed{Default: &Default{Client: up.Server.Client()}, Tokens: &zedTokens{tokens: []string{"L"}}}
	req := zedReq(up, upstream.FormatGemini)
	req.Body = []byte(`{"contents":[],"safetySettings":[{"category":"X"}]}`)
	if _, err := z.Execute(context.Background(), req); err != nil {
		t.Fatalf("%v", err)
	}
	if gjson.GetBytes(up.Calls()[0].Body, "provider_request.safetySettings").Exists() {
		t.Errorf("the client's settings were sent: %s", up.Calls()[0].Body)
	}
}

func TestZedAnEmptyProviderRequestSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	z := &Zed{Default: &Default{Client: up.Server.Client()}, Tokens: &zedTokens{tokens: []string{"L"}}}
	req := zedReq(up, upstream.FormatClaude)
	req.Body = nil
	if _, err := z.Execute(context.Background(), req); !errors.Is(err, ErrBadAccount) {
		t.Fatalf("%v", err)
	}
	if len(up.Calls()) != 0 {
		t.Fatal("a call went out")
	}
}

func TestZedARefusedTokenIsReplacedOnce(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 401}, tests.Reply{Body: "{}"})
	tokens := &zedTokens{tokens: []string{"OLD", "NEW"}}
	z := &Zed{Default: &Default{Client: up.Server.Client()}, Tokens: tokens}
	if _, err := z.Execute(context.Background(), zedReq(up, upstream.FormatClaude)); err != nil {
		t.Fatalf("%v", err)
	}
	if len(tokens.forced) != 2 || tokens.forced[1] != true {
		t.Fatalf("forced flags %v", tokens.forced)
	}
	if got := up.Calls()[1].Header.Get("Authorization"); got != "Bearer NEW" {
		t.Errorf("second call carried %q", got)
	}
}

func TestZedAnAccountWithoutATokenSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	z := &Zed{Default: &Default{Client: up.Server.Client()}, Tokens: &zedTokens{tokens: []string{"L"}}}
	req := zedReq(up, upstream.FormatClaude)
	req.Key = ""
	if _, err := z.Execute(context.Background(), req); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("%v", err)
	}
	if len(up.Calls()) != 0 {
		t.Fatal("a call went out")
	}
}

func TestZedThePayloadCarriesTheSessionAndThePrompt(t *testing.T) {
	out, err := zedPayload(Request{Model: "m", Session: "conv-1", Body: []byte(`{"prompt_id":"p1"}`)}, "anthropic", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(out, "thread_id").String(); got != "conv-1" {
		t.Errorf("thread %q", got)
	}
	if got := gjson.GetBytes(out, "prompt_id").String(); got != "p1" {
		t.Errorf("prompt %q", got)
	}
	if !strings.Contains(string(out), `"provider_request":{}`) {
		t.Errorf("payload %s", out)
	}
}
