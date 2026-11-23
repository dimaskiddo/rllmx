package pipeline

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"rllmx/app/platform/ratelimit"
	"rllmx/app/store/analytics"
	"rllmx/tests"
)

type pacerSpy struct {
	calls int
	keys  []string
	err   error
}

func (p *pacerSpy) Wait(_ context.Context, key string) error {
	p.calls++
	p.keys = append(p.keys, key)
	return p.err
}

func TestAConnectionIsPacedOnceBeforeItsSend(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	conn := r.conn("forced", "k1")
	pacer := &pacerSpy{}
	r.p.d.Pacer = pacer
	out, _ := r.do("/v1/chat/completions", `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if pacer.calls != 1 {
		t.Fatalf("%d waits, want one per attempt", pacer.calls)
	}
	if len(pacer.keys) != 1 || pacer.keys[0] != conn.ID {
		t.Fatalf("paced %v, want the connection that was sent to (%s)", pacer.keys, conn.ID)
	}
	if len(r.up.Calls()) != 1 {
		t.Fatalf("%d upstream calls, want 1", len(r.up.Calls()))
	}
}

func TestARefusedPaceAnswersWithoutSending(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.p.d.Pacer = &pacerSpy{err: ratelimit.ErrWait}
	out, _ := r.do("/v1/chat/completions", `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if out.Header.Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q", out.Header.Get("Retry-After"))
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("a queued request reached the provider")
	}
	if last := r.last(); last.Status != http.StatusServiceUnavailable || last.ErrorClass != "capacity" {
		t.Fatalf("row = %d %q, want a capacity refusal", last.Status, last.ErrorClass)
	}
}

func TestTheAnswerLineNamesTheFeaturesThatActed(t *testing.T) {
	line := answerLine(analytics.Record{
		RequestID: "rid-7", Provider: "claude", Model: "claude-opus-5-5", ConnectionID: "c1",
		Attempt: 2, Status: 200, LatencyMs: 840, Savers: "html,headroom", Skills: "ponytail",
	})
	for _, want := range []string{"answer ", "id=rid-7", "provider=claude", "model=claude-opus-5-5",
		"conn=c1", "attempt=2", "status=200", "ms=840", "savers=html,headroom", "skills=ponytail"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line lacks %q: %s", want, line)
		}
	}
}

func TestOnlyAFeatureChangeEarnsTheAnswerLine(t *testing.T) {
	if answerLogged(analytics.Record{Status: 200}) {
		t.Fatal("a plain request earned a feature line")
	}
	if !answerLogged(analytics.Record{Savers: "html"}) || !answerLogged(analytics.Record{Skills: "ponytail"}) {
		t.Fatal("a request a saver or a skill changed did not earn its line")
	}
}
