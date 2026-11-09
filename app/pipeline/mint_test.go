package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/oauth"
	"rllmx/app/repository"
	"rllmx/tests"
)

// fakeMinter hands out the tokens it is told to and counts its calls.
type fakeMinter struct {
	mu       sync.Mutex
	tokens   []string
	token    int
	renews   int
	rejected []string
	err      error
}

func (f *fakeMinter) Token(context.Context, repository.Connection) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token++
	if f.err != nil {
		return "", f.err
	}
	return f.tokens[0], nil
}

func (f *fakeMinter) Renew(_ context.Context, _ repository.Connection, rejected string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renews++
	f.rejected = append(f.rejected, rejected)
	if f.err != nil {
		return "", f.err
	}
	return f.tokens[min(f.renews, len(f.tokens)-1)], nil
}

func (r *rig) copilot(email string) repository.Connection {
	r.t.Helper()
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "cop", AuthType: "oauth", Email: email,
		Fields: map[string]string{"accessToken": `"GH-ACCESS"`, "testStatus": `"active"`},
	}, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

const copilotChat = `{"model":"cop/cm-chat","messages":[{"role":"user","content":"hi"}]}`

func TestAMintedTokenIsWhatARequestCarriesNotTheLoginsOwn(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Minter = &fakeMinter{tokens: []string{"COPILOT-1"}}
	r.copilot("a@x.test")
	out, _ := r.do("/v1/chat/completions", copilotChat)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	call := r.up.Calls()[0]
	if auth(call) != "Bearer COPILOT-1" || strings.Contains(strings.Join(call.Header.Values("Authorization"), ","), "GH-ACCESS") {
		t.Fatalf("auth %q", auth(call))
	}
	if call.Header.Get("Copilot-Integration-Id") != "vscode-chat" || call.Header.Get("X-Initiator") != "user" || call.Header.Get("X-Request-Id") == "" {
		t.Fatalf("%v", call.Header)
	}
	if call.Path != "/chat/completions" {
		t.Fatalf("path %s", call.Path)
	}
}

func TestEachRequestCarriesItsOwnRequestID(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Minter = &fakeMinter{tokens: []string{"COPILOT-1"}}
	r.copilot("a@x.test")
	r.do("/v1/chat/completions", copilotChat)
	r.do("/v1/chat/completions", copilotChat)
	calls := r.up.Calls()
	if len(calls) != 2 || calls[0].Header.Get("X-Request-Id") == calls[1].Header.Get("X-Request-Id") {
		t.Fatalf("request ids: %v", calls)
	}
}

func TestTheWireFormatAndTheAddressFollowTheModel(t *testing.T) {
	for _, tc := range []struct{ model, path, field string }{
		{"cm-chat", "/chat/completions", "messages"},
		{"cm-claude", "/v1/messages", "messages"},
		{"cm-resp", "/responses", "input"},
	} {
		reply := tests.Reply{ContentType: "application/json", Body: okJSON}
		r := newRig(t, reply)
		r.p.d.Minter = &fakeMinter{tokens: []string{"C"}}
		r.copilot("a@x.test")
		r.run("/v1/chat/completions", `{"model":"cop/`+tc.model+`","messages":[{"role":"user","content":"hi"}]}`)
		calls := r.up.Calls()
		if len(calls) != 1 || calls[0].Path != tc.path || !gjson.GetBytes(calls[0].Body, tc.field).Exists() {
			t.Fatalf("%s: %d calls, path %q, body %s", tc.model, len(calls), calls[0].Path, calls[0].Body)
		}
	}
}

// A Claude-named model reaches the Claude address even when the data does not
// list it: the live catalog gets new ones first.
func TestAClaudeNamedModelTheDataDoesNotListStillGoesToTheClaudeAddress(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Minter = &fakeMinter{tokens: []string{"C"}}
	r.copilot("a@x.test")
	r.run("/v1/chat/completions", `{"model":"cop/claude-opus-9","messages":[{"role":"user","content":"hi"}]}`)
	calls := r.up.Calls()
	if len(calls) != 1 || calls[0].Path != "/v1/messages" {
		t.Fatalf("%d calls, path %q", len(calls), calls[0].Path)
	}
}

func TestTheClaudeNameIsMatchedWhateverTheCase(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Minter = &fakeMinter{tokens: []string{"C"}}
	r.copilot("a@x.test")
	r.run("/v1/chat/completions", `{"model":"cop/Claude-Opus-9","messages":[{"role":"user","content":"hi"}]}`)
	if calls := r.up.Calls(); len(calls) != 1 || calls[0].Path != "/v1/messages" {
		t.Fatalf("%v", calls)
	}
}

// Only Copilot picks its address by the model's name; a model of another
// provider that happens to say claude stays on that provider's own address.
func TestAClaudeNamedModelOfAnotherProviderIsNotRedirected(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.accountConn("wire", "k1", "")
	r.run("/v1/chat/completions", `{"model":"wire/claude-like","messages":[{"role":"user","content":"hi"}]}`)
	calls := r.up.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	// The Claude codec writes `max_tokens` and a `system` field; the OpenAI one
	// keeps `messages` with a user role. The body tells which codec wrote it.
	if gjson.GetBytes(calls[0].Body, "messages.0.role").String() != "user" || gjson.GetBytes(calls[0].Body, "system").Exists() {
		t.Fatalf("body %s", calls[0].Body)
	}
	if gjson.GetBytes(calls[0].Body, "max_tokens").Exists() {
		t.Fatalf("a Claude body was written for another provider: %s", calls[0].Body)
	}
}

func TestANonClaudeUnlistedModelStaysOnTheChatAddress(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Minter = &fakeMinter{tokens: []string{"C"}}
	r.copilot("a@x.test")
	r.run("/v1/chat/completions", `{"model":"cop/gemini-9","messages":[{"role":"user","content":"hi"}]}`)
	if calls := r.up.Calls(); len(calls) != 1 || calls[0].Path != "/chat/completions" {
		t.Fatalf("%v", calls)
	}
}

func TestARefusedMintedTokenIsReplacedOnceAndNeverSentAgain(t *testing.T) {
	for _, status := range []int{401, 403} {
		r := newRig(t, tests.Reply{Status: status, Body: `{"error":{"message":"expired"}}`}, tests.Reply{ContentType: "application/json", Body: okJSON})
		m := &fakeMinter{tokens: []string{"COPILOT-1", "COPILOT-2"}}
		r.p.d.Minter = m
		r.copilot("a@x.test")
		out, _ := r.do("/v1/chat/completions", copilotChat)
		calls := r.up.Calls()
		if out.Status != 200 || len(calls) != 2 || auth(calls[0]) != "Bearer COPILOT-1" || auth(calls[1]) != "Bearer COPILOT-2" {
			t.Fatalf("%d: %d %d calls", status, out.Status, len(calls))
		}
		if m.renews != 1 || m.rejected[0] != "COPILOT-1" {
			t.Fatalf("renews %d, rejected %v", m.renews, m.rejected)
		}
	}
}

func TestASecondRefusalOfTheMintedTokenFailsTheAccount(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 401, Body: `{"error":{"message":"nope"}}`})
	m := &fakeMinter{tokens: []string{"COPILOT-1", "COPILOT-2"}}
	r.p.d.Minter = m
	r.copilot("a@x.test")
	out, _ := r.do("/v1/chat/completions", copilotChat)
	if out.Status != 502 || len(r.up.Calls()) != 2 || m.renews != 1 {
		t.Fatalf("%d, %d calls, %d renews", out.Status, len(r.up.Calls()), m.renews)
	}
}

func TestAMintThatFailsSendsNothingAndLeaksNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"rejected":  {&oauth.Error{Class: oauth.ClassRejected}, 502},
		"transport": {&oauth.Error{Class: oauth.ClassTransport}, 502},
		"no token":  {oauth.ErrNoToken, 502},
	} {
		r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
		r.p.d.Minter = &fakeMinter{err: tc.err}
		r.copilot("a@x.test")
		out, _ := r.do("/v1/chat/completions", copilotChat)
		if out.Status != tc.status || len(r.up.Calls()) != 0 {
			t.Fatalf("%s: %d, %d calls", name, out.Status, len(r.up.Calls()))
		}
		if strings.Contains(string(out.Body), "GH-ACCESS") {
			t.Fatalf("%s: %s", name, out.Body)
		}
	}
}

func TestAMintFailureMovesOnToTheNextAccount(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Minter = &fakeMinter{err: errors.New("down")}
	r.copilot("a@x.test")
	r.copilot("b@x.test")
	r.do("/v1/chat/completions", copilotChat)
	if len(r.up.Calls()) != 0 {
		t.Fatalf("%d calls", len(r.up.Calls()))
	}
}

func TestAKeyOnAMintingProviderIsSentAsItIs(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	m := &fakeMinter{tokens: []string{"never"}}
	r.p.d.Minter = m
	if _, err := r.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "cop", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"sk-1"`},
	}, nil); err != nil {
		t.Fatal(err)
	}
	r.do("/v1/chat/completions", copilotChat)
	if m.token != 0 || m.renews != 0 || auth(r.up.Calls()[0]) != "Bearer sk-1" {
		t.Fatalf("minter used for a key: %d %d %q", m.token, m.renews, auth(r.up.Calls()[0]))
	}
}
