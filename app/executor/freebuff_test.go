package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/upstream"
	"rllmx/tests"
)

// freebuffSessions is a scripted SessionSource.
type freebuffSessions struct {
	id    string
	err   error
	asked []bool
	mu    sync.Mutex
}

func (f *freebuffSessions) Claim(_ context.Context, _, _ string, fresh bool) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, fresh)
	return f.id, f.err
}

func freebuffReq(up *tests.Upstream) Request {
	return Request{URL: up.URL + "/api/v1/chat/completions", Key: "tok", Model: "deepseek/deepseek-v4-flash",
		Session: "fp-1", Body: []byte(`{"messages":[{"role":"user","content":"hi"}],"stream":true}`),
		Entry: upstream.Entry{Executor: upstream.ExecutorFreebuff}}
}

func TestFreebuffRegistersARunBeforeTheCallAndClosesItAfter(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Body: `{"runId":"run-1"}`},
		tests.Reply{Body: `{"ok":true}`},
		tests.Reply{Body: `{}`},
	)
	f := &Freebuff{Default: &Default{Client: up.Server.Client()}, Sessions: &freebuffSessions{id: "inst-1"}}
	resp, err := f.Execute(context.Background(), freebuffReq(up))
	if err != nil {
		t.Fatalf("%v", err)
	}
	resp.Body.Close()
	calls := up.Calls()
	if got := gjson.GetBytes(calls[0].Body, "action").String(); got != "START" {
		t.Errorf("first call was %q, want the run start", got)
	}
	if got := gjson.GetBytes(calls[0].Body, "agentId").String(); got != "base3-free-deepseek-flash" {
		t.Errorf("agent %q", got)
	}
	if got := gjson.GetBytes(calls[1].Body, "codebuff_metadata.run_id").String(); got != "run-1" {
		t.Errorf("run id %q", got)
	}
	if got := gjson.GetBytes(calls[1].Body, "codebuff_metadata.freebuff_instance_id").String(); got != "inst-1" {
		t.Errorf("instance %q", got)
	}
	if got := gjson.GetBytes(calls[1].Body, "provider.allow_fallbacks"); got.Bool() {
		t.Error("fallbacks were allowed")
	}
}

func TestFreebuffPutsTheAssistantsMarkerAndTheClosingTool(t *testing.T) {
	body := freebuffBody(Request{Body: []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"read"}}]}`), Session: "fp"}, "")
	system := gjson.GetBytes(body, `messages.#(role=="system").content`).String()
	if !strings.HasPrefix(system, freebuffMarker) {
		t.Errorf("the first turn is %q", system)
	}
	names := []string{}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	if len(names) != 2 || names[1] != "end_turn" {
		t.Errorf("tools %v", names)
	}
	// A request that already carries the opening is left as it is.
	again := freebuffBody(Request{Body: body, Session: "fp"}, "")
	if n := strings.Count(string(again), freebuffMarker); n != 1 {
		t.Errorf("the marker appears %d times", n)
	}
	own := freebuffBody(Request{Body: []byte(`{"messages":[{"role":"system","content":"` + freebuffOpenings[1] + `"}]}`)}, "")
	if got := gjson.GetBytes(own, "messages.0.content").String(); got != freebuffOpenings[1] {
		t.Errorf("an accepted opening was rewritten: %q", got)
	}
}

func TestFreebuffARefusedSessionSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	f := &Freebuff{Default: &Default{Client: up.Server.Client()}, Sessions: &freebuffSessions{err: ErrAccountSession}}
	if _, err := f.Execute(context.Background(), freebuffReq(up)); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("%v", err)
	}
	if len(up.Calls()) != 0 {
		t.Fatal("a call went out")
	}
}

func TestFreebuffARunThatCannotStartSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Status: 500})
	f := &Freebuff{Default: &Default{Client: up.Server.Client()}}
	if _, err := f.Execute(context.Background(), freebuffReq(up)); err == nil {
		t.Fatal("a refused run start was accepted")
	}
	if len(up.Calls()) != 1 {
		t.Fatalf("%d calls, want only the refused start", len(up.Calls()))
	}
}

func TestFreebuffAKeylessCallSendsNothing(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{"runId":"r"}`})
	f := &Freebuff{Default: &Default{Client: up.Server.Client()}}
	req := freebuffReq(up)
	req.Key = ""
	if _, err := f.Execute(context.Background(), req); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("%v", err)
	}
	if len(up.Calls()) != 0 {
		t.Fatal("a call went out")
	}
}
