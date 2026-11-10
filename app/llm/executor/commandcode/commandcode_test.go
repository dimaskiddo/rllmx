package commandcode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

func commandCodeReq(up *tests.Upstream) executor.Request {
	return executor.Request{URL: up.URL, Key: "user_1", Body: []byte(`{"threadId":"t-1","params":{}}`), Auth: llm.Bearer(),
		Entry: upstream.Entry{Transport: upstream.Transport{Headers: map[string]string{
			"User-Agent": "cli", "x-command-code-version": "1.88.0", "x-cli-environment": "production",
		}}}}
}

func TestCommandCodeSendsTheThreadAndTheFixedWorkspace(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{\"type\":\"text-delta\",\"text\":\"hi\"}\n"})
	c := &CommandCode{Default: &executor.Default{Client: up.Server.Client()}}
	resp, err := c.Execute(context.Background(), commandCodeReq(up))
	if err != nil {
		t.Fatalf("%v", err)
	}
	resp.Body.Close()
	call := up.Calls()[0]
	for name, want := range map[string]string{
		"X-Session-Id": "t-1", "X-Project-Slug": "workspace",
		"User-Agent": "cli", "X-Command-Code-Version": "1.88.0",
		"Authorization": "Bearer user_1",
	} {
		if got := call.Header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if strings.Contains(call.Header.Get("X-Project-Slug"), "/") {
		t.Errorf("the host's path reached the wire: %q", call.Header.Get("X-Project-Slug"))
	}
}

func TestCommandCodeAnErrorEventBeforeOutputBecomesItsStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		status int
	}{
		"a named status": {`{"type":"error","error":{"statusCode":429,"message":"slow down"}}`, http.StatusTooManyRequests},
		"a keyword":      {`{"type":"error","error":"invalid api key"}`, http.StatusUnauthorized},
		"an unknown one": {`{"type":"error","error":"something odd"}`, http.StatusServiceUnavailable},
	} {
		up := tests.NewUpstream(t, tests.Reply{Body: tc.body + "\n"})
		var waits []time.Duration
		c := &CommandCode{Default: &executor.Default{Client: up.Server.Client(), Sleep: tests.InstantSleep(&waits)}}
		resp, err := c.Execute(context.Background(), commandCodeReq(up))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if resp.Status != tc.status {
			t.Errorf("%s: status %d, want %d", name, resp.Status, tc.status)
		}
		resp.Body.Close()
	}
}

func TestCommandCodeABusyAnswerIsTriedAgainAndThenAnswered(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Body: `{"type":"error","error":{"statusCode":503,"message":"overloaded"}}` + "\n"},
		tests.Reply{Body: `{"type":"error","error":{"statusCode":503,"message":"overloaded"}}` + "\n"},
		tests.Reply{Body: `{"type":"text-delta","text":"ok"}` + "\n"},
	)
	var waits []time.Duration
	c := &CommandCode{Default: &executor.Default{Client: up.Server.Client(), Sleep: tests.InstantSleep(&waits)}}
	resp, err := c.Execute(context.Background(), commandCodeReq(up))
	if err != nil {
		t.Fatalf("%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(up.Calls()) != 3 || !strings.Contains(string(body), "ok") {
		t.Fatalf("%d calls, body %s", len(up.Calls()), body)
	}
}

func TestCommandCodeACleanAnswerIsReplayedWhole(t *testing.T) {
	payload := "{\"type\":\"text-delta\",\"text\":\"hi\"}\n{\"type\":\"finish\"}\n"
	up := tests.NewUpstream(t, tests.Reply{Body: payload})
	c := &CommandCode{Default: &executor.Default{Client: up.Server.Client()}}
	resp, err := c.Execute(context.Background(), commandCodeReq(up))
	if err != nil {
		t.Fatalf("%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != payload {
		t.Errorf("replayed %q, want %q", body, payload)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content type %q", got)
	}
}

func TestCommandCodeRefusesAKeylessCallAndABodyWithoutAThread(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	c := &CommandCode{Default: &executor.Default{Client: up.Server.Client()}}
	req := commandCodeReq(up)
	req.Key = ""
	if _, err := c.Execute(context.Background(), req); !errors.Is(err, executor.ErrNoCredential) {
		t.Errorf("no key: %v", err)
	}
	req = commandCodeReq(up)
	req.Body = []byte(`{}`)
	if _, err := c.Execute(context.Background(), req); !errors.Is(err, executor.ErrBadAccount) {
		t.Errorf("no thread: %v", err)
	}
	if len(up.Calls()) != 0 {
		t.Fatalf("%d calls", len(up.Calls()))
	}
}
