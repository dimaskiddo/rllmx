package pipeline

import (
	"context"
	"net/http"
	"testing"

	"rllmx/app/platform/ratelimit"
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
