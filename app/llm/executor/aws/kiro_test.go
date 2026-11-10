package aws

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/app/protocol/eventstream"
	"rllmx/tests"
)

// kiroEvent builds one frame of the conversation stream.
func kiroEvent(name, payload string) string {
	return string(eventstream.Encode(map[string]string{":event-type": name, ":message-type": "event"}, []byte(payload)))
}

// kiroUpstream answers with a scripted event stream.
func kiroUpstream(t *testing.T, frames ...string) *tests.Upstream {
	t.Helper()
	return tests.NewUpstream(t, tests.Reply{
		ContentType: "application/vnd.amazon.eventstream",
		Body:        strings.Join(frames, ""),
	})
}

func kiroRequest(t *testing.T, up *tests.Upstream, body string) executor.Request {
	t.Helper()
	return executor.Request{
		Key: "TOKEN", Model: "claude-sonnet-4.5", Stream: true, Body: []byte(body),
		Account: tests.Account(map[string]string{"authMethod": "builder-id"}),
		Entry:   upstream.Entry{Transport: upstream.Transport{BaseURLs: []string{strings.Replace(up.URL, "http://", "https://", 1)}}},
	}
}

func kiroClient(t *testing.T, up *tests.Upstream) *http.Client {
	t.Helper()
	return &http.Client{Transport: tests.Redirect(func(r *http.Request) {
		r.URL.Scheme, r.URL.Host = "http", strings.TrimPrefix(up.URL, "http://")
	})}
}

func TestKiroHeadersMarkTheCredentialKind(t *testing.T) {
	for name, tc := range map[string]struct {
		method, wantType, wantTarget string
	}{
		"api key":     {"api_key", "API_KEY", ""},
		"external id": {"external_idp", "EXTERNAL_IDP", ""},
		"builder id":  {"builder-id", "", ""},
	} {
		host := "https://q.us-east-1.amazonaws.com"
		if tc.method == "api_key" {
			host = "https://q.us-east-1.amazonaws.com"
		}
		h := kiroHeaders(host, kiroAuth{method: tc.method, token: "T"}, "")
		if got := h["TokenType"]; got != tc.wantType {
			t.Errorf("%s: TokenType %q, want %q", name, got, tc.wantType)
		}
		if h["Authorization"] != "Bearer T" || h["x-amz-sso-bearer"] != "T" {
			t.Errorf("%s: auth headers %v", name, h)
		}
		if h["x-amzn-codewhisperer-machine-id"] != "kiro-desktop" {
			t.Errorf("%s: machine id %q", name, h["x-amzn-codewhisperer-machine-id"])
		}
	}
}

func TestTheCodeWhispererHostAsksForItsTarget(t *testing.T) {
	h := kiroHeaders("https://codewhisperer.us-east-1.amazonaws.com", kiroAuth{method: "builder-id", token: "T"}, "")
	if h["X-Amz-Target"] != kiroTarget {
		t.Errorf("target %q", h["X-Amz-Target"])
	}
	if _, ok := kiroHeaders("https://q.us-east-1.amazonaws.com", kiroAuth{token: "T"}, "")["X-Amz-Target"]; ok {
		t.Error("the Amazon Q host was sent a target")
	}
}

func TestKiroHostOrderPutsTheAmazonQHostFirstForAnAPIKey(t *testing.T) {
	e := upstream.Entry{Transport: upstream.Transport{BaseURLs: []string{
		"https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
		"https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
		"https://q.us-east-1.amazonaws.com/generateAssistantResponse",
	}}}
	for name, tc := range map[string]struct {
		method string
		first  string
		last   string
	}{
		"api key":    {"api_key", "https://q.", "https://runtime."},
		"builder id": {"builder-id", "https://codewhisperer.", "https://q."},
	} {
		got := kiroHosts(e, tc.method, "us-east-1")
		if !strings.HasPrefix(got[0], tc.first) || !strings.HasPrefix(got[len(got)-1], tc.last) {
			t.Errorf("%s: %v", name, got)
		}
		if strings.HasSuffix(got[0], "/generateAssistantResponse") {
			t.Errorf("%s: the path was kept: %q", name, got[0])
		}
	}
}

func TestKiroHostsMoveToTheAccountsRegion(t *testing.T) {
	e := upstream.Entry{Transport: upstream.Transport{BaseURLs: []string{
		"https://q.us-east-1.amazonaws.com/generateAssistantResponse",
		"https://runtime.us-east-1.kiro.dev/generateAssistantResponse",
	}}}
	got := strings.Join(kiroHosts(e, "builder-id", "eu-west-1"), " ")
	if !strings.Contains(got, "https://q.eu-west-1.amazonaws.com") {
		t.Errorf("the amazon host was not regionalised: %s", got)
	}
	if !strings.Contains(got, "https://runtime.us-east-1.kiro.dev") {
		t.Errorf("the kiro.dev host was regionalised: %s", got)
	}
}

func TestACompleteKiroTurnIsReplayedToThePipeline(t *testing.T) {
	up := kiroUpstream(t,
		kiroEvent("assistantResponseEvent", `{"content":"hi"}`),
		kiroEvent("messageStopEvent", `{"stopReason":"end_turn"}`),
	)
	k := &Kiro{Default: &executor.Default{Client: kiroClient(t, up)}}
	resp, err := k.Execute(context.Background(), kiroRequest(t, up, `{"conversationState":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("content type %q", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), "assistantResponseEvent") || !strings.Contains(string(body), "hi") {
		t.Errorf("body %q", body)
	}
}

func TestAnUnfinishedKiroTurnIsAskedOnceMore(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{ContentType: "application/vnd.amazon.eventstream",
			Body: kiroEvent("assistantResponseEvent", `{"content":"..."}`) + kiroEvent("messageStopEvent", `{"stopReason":"end_turn"}`)},
		tests.Reply{ContentType: "application/vnd.amazon.eventstream",
			Body: kiroEvent("assistantResponseEvent", `{"content":"the answer"}`) + kiroEvent("messageStopEvent", `{"stopReason":"end_turn"}`)},
	)
	k := &Kiro{Default: &executor.Default{Client: kiroClient(t, up)}}
	resp, err := k.Execute(context.Background(), kiroRequest(t, up, `{"conversationState":{"currentMessage":{"userInputMessage":{"content":"hi"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(up.Calls()) != 2 {
		t.Fatalf("%d calls, want the turn asked once more", len(up.Calls()))
	}
	if !strings.Contains(string(up.Calls()[1].Body), "ellipsis") {
		t.Errorf("the repair did not reach the second call: %s", up.Calls()[1].Body)
	}
	if !strings.Contains(string(body), "the answer") {
		t.Errorf("the second answer was not sent: %q", body)
	}
}

func TestARepairIsTriedOnlyOnceAndAFailureIsAnswered(t *testing.T) {
	up := kiroUpstream(t,
		kiroEvent("assistantResponseEvent", `{"content":"..."}`),
		kiroEvent("messageStopEvent", `{"stopReason":"end_turn"}`),
	)
	k := &Kiro{Default: &executor.Default{Client: kiroClient(t, up)}}
	resp, err := k.Execute(context.Background(), kiroRequest(t, up, `{"conversationState":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(up.Calls()) != 2 {
		t.Fatalf("%d calls, want one repair attempt", len(up.Calls()))
	}
	if !strings.Contains(string(body), "error") {
		t.Errorf("a turn that could not be repaired was sent as an answer: %q", body)
	}
}

func TestAKiroTurnCanHaveTheRepairTurnedOff(t *testing.T) {
	up := kiroUpstream(t,
		kiroEvent("assistantResponseEvent", `{"content":"..."}`),
		kiroEvent("messageStopEvent", `{"stopReason":"end_turn"}`),
	)
	k := &Kiro{Default: &executor.Default{Client: kiroClient(t, up)}}
	req := kiroRequest(t, up, `{"conversationState":{}}`)
	req.Account = tests.Account(map[string]string{"authMethod": "builder-id", "kiroToolCallRepair": "false"})
	if _, err := k.Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(up.Calls()) != 1 {
		t.Errorf("%d calls, want no repair", len(up.Calls()))
	}
}

func TestKiroFallsToTheNextHostOnAnAuthRefusal(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Status: http.StatusUnauthorized, Body: `{}`},
		tests.Reply{ContentType: "application/vnd.amazon.eventstream", Body: kiroEvent("assistantResponseEvent", `{"content":"ok"}`) + kiroEvent("messageStopEvent", `{}`)},
	)
	e := upstream.Entry{Transport: upstream.Transport{BaseURLs: []string{
		"https://q.us-east-1.amazonaws.com/generateAssistantResponse",
		"https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse",
	}}}
	k := &Kiro{Default: &executor.Default{Client: kiroClient(t, up)}}
	req := kiroRequest(t, up, `{"conversationState":{}}`)
	req.Entry = e
	resp, err := k.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(up.Calls()) != 2 || !strings.Contains(string(body), "ok") {
		t.Fatalf("%d calls, body %q", len(up.Calls()), body)
	}
}

func TestKiroRefusesAMissingCredential(t *testing.T) {
	k := &Kiro{Default: &executor.Default{Client: http.DefaultClient}}
	if _, err := k.Execute(context.Background(), executor.Request{Model: "m"}); !errors.Is(err, executor.ErrNoCredential) {
		t.Errorf("%v", err)
	}
}
