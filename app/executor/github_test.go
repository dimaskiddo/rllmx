package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
	"rllmx/tests"
)

func copilotReq(url, body string) Request {
	return Request{
		URL: url, Body: []byte(body), Key: "COPILOT-TOKEN",
		Auth: &upstream.Auth{Scheme: upstream.SchemeBearer, Header: "Authorization", Headers: map[string]string{"X-Initiator": "user"}},
	}
}

func TestACopilotCallCarriesTheMintedTokenAndItsOwnRequestID(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	g := &GitHub{Default: &Default{Client: http.DefaultClient}}
	for range 2 {
		resp, err := g.Execute(context.Background(), copilotReq(up.URL+"/chat/completions", `{"messages":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	calls := up.Calls()
	if calls[0].Header.Get("Authorization") != "Bearer COPILOT-TOKEN" || calls[0].Header.Get("X-Initiator") != "user" {
		t.Fatalf("%v", calls[0].Header)
	}
	a, b := calls[0].Header.Get("X-Request-Id"), calls[1].Header.Get("X-Request-Id")
	if a == "" || a == b {
		t.Fatalf("request ids %q %q", a, b)
	}
}

func TestACopilotCallWithoutACredentialSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t)
	g := &GitHub{Default: &Default{Client: http.DefaultClient}}
	req := copilotReq(up.URL+"/chat/completions", `{}`)
	req.Key = ""
	if _, err := g.Execute(context.Background(), req); err != ErrNoCredential || len(up.Calls()) != 0 {
		t.Fatalf("%v, %d calls", err, len(up.Calls()))
	}
}

func TestACopilotChatBodyIsFittedToTheEndpoint(t *testing.T) {
	const in = `{"model":"m","reasoning_effort":"none","messages":[` +
		`{"role":"system","content":"plain text stays"},` +
		`{"role":"user","content":[{"type":"text","text":"keep"},{"type":"text","text":""},{"type":"image_url","image_url":{"url":"https://h/i.png"}},{"type":"input_audio","input_audio":{"data":"AAA"}},{"type":"file","content":"as text"}]},` +
		`{"role":"user","content":[{"type":"text","text":""}]},` +
		`{"role":"assistant","content":null}]}`
	out := fitCopilot([]byte(in))
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("effort none was sent: %s", out)
	}
	msgs := gjson.GetBytes(out, "messages").Array()
	if len(msgs) != 4 || msgs[0].Get("content").String() != "plain text stays" || msgs[3].Get("content").Type != gjson.Null {
		t.Fatalf("%s", out)
	}
	parts := msgs[1].Get("content").Array()
	if len(parts) != 4 || parts[0].Get("text").String() != "keep" || parts[1].Get("type").String() != "image_url" ||
		parts[2].Get("type").String() != "text" || !strings.Contains(parts[2].Get("text").String(), "input_audio") ||
		parts[3].Get("text").String() != "as text" {
		t.Fatalf("%s", msgs[1].Raw)
	}
	if msgs[2].Get("content").Type != gjson.Null {
		t.Fatalf("a message left with no part has %s", msgs[2].Raw)
	}
}

func TestACopilotBodyThatNeedsNoFitIsReturnedUntouched(t *testing.T) {
	for _, in := range []string{
		`{"messages":[{"role":"user","content":"hi"},{"role":"user","content":[{"type":"text","text":"a"}]}],"n":1.50}`,
		`{"model":"m"}`,
		`[1,2]`,
		`not json`,
		`{"messages":"x"}`,
	} {
		if got := string(fitCopilot([]byte(in))); got != in {
			t.Errorf("%s became %s", in, got)
		}
	}
}

func TestOnlyTheChatEndpointGetsTheFittedBody(t *testing.T) {
	const in = `{"reasoning_effort":"none","messages":[{"role":"user","content":[{"type":"text","text":""}]}]}`
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	g := &GitHub{Default: &Default{Client: http.DefaultClient}}
	for _, path := range []string{"/chat/completions", "/responses", "/v1/messages"} {
		resp, err := g.Execute(context.Background(), copilotReq(up.URL+path, in))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	calls := up.Calls()
	if gjson.GetBytes(calls[0].Body, "reasoning_effort").Exists() {
		t.Fatalf("chat: %s", calls[0].Body)
	}
	for _, c := range calls[1:] {
		if string(c.Body) != in {
			t.Errorf("%s was changed: %s", c.Path, c.Body)
		}
	}
}

func FuzzFitCopilot(f *testing.F) {
	for _, s := range []string{`{}`, `{"messages":[{"content":[{"type":"x"}]}]}`, `{"messages":[null,1,[]]}`, `{"messages":[{"content":[{}]}],"reasoning_effort":"none"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		out := fitCopilot(in)
		if gjson.ValidBytes(in) && !gjson.ValidBytes(out) {
			t.Fatalf("valid input %q became invalid %q", in, out)
		}
		if !gjson.ValidBytes(in) && string(out) != string(in) {
			t.Fatalf("invalid input changed: %q", in)
		}
	})
}

func TestAMuxServesTheCopilotExecutor(t *testing.T) {
	if _, ok := NewMux(&Default{}, Options{}).byName[upstream.ExecutorGitHub]; !ok {
		t.Fatal("github is not registered")
	}
}

func TestEveryCopilotAddressIsChosenByWireFormat(t *testing.T) {
	e := upstream.Entry{Transport: upstream.Transport{
		BaseURL: "https://h/chat/completions", ResponsesURL: "https://h/responses", MessagesURL: "https://h/v1/messages", URLSuffix: "?v=1",
	}}
	for format, want := range map[string]string{
		upstream.FormatOpenAI:    "https://h/chat/completions?v=1",
		upstream.FormatResponses: "https://h/responses?v=1",
		upstream.FormatClaude:    "https://h/v1/messages?v=1",
	} {
		if got := RegistryURL(e, format, "m", false); got != want {
			t.Errorf("%s: %s, want %s", format, got, want)
		}
	}
	plain := upstream.Entry{Transport: upstream.Transport{BaseURL: "https://h/chat/completions"}}
	if got := RegistryURL(plain, upstream.FormatClaude, "m", false); got != "https://h/chat/completions" {
		t.Errorf("a provider with one address: %s", got)
	}
}

// The operator's header overrides apply to the chat address only, as in the
// reference; the other two endpoints get the fixed identity set alone.
func TestHeaderOverridesReachOnlyTheCopilotChatAddress(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`}, tests.Reply{Body: `{}`})
	g := &GitHub{Default: &Default{Client: http.DefaultClient}}
	for _, path := range []string{"/chat/completions", "/responses", "/v1/messages"} {
		req := copilotReq(up.URL+path, `{"messages":[]}`)
		req.Overrides = map[string]string{"X-Operator": "yes"}
		resp, err := g.Execute(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	calls := up.Calls()
	if calls[0].Header.Get("X-Operator") != "yes" || calls[1].Header.Get("X-Operator") != "" || calls[2].Header.Get("X-Operator") != "" {
		t.Fatalf("%v %v %v", calls[0].Header, calls[1].Header, calls[2].Header)
	}
	for _, c := range calls {
		if c.Header.Get("X-Initiator") != "user" {
			t.Errorf("the identity set is missing on %s: %v", c.Path, c.Header)
		}
	}
}
