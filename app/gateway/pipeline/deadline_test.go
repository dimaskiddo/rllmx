package pipeline

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/access/identity"
	"rllmx/app/llm/stream"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// A caller that gave up (a probe's 10 s, the router's 8 s) is not the account's
// problem: an upstream that was merely slow must keep its lock, no other
// account can be tried in the time left, and the write on a dead context only
// produces a failed query and a warning.
func TestAFailurePastTheCallersDeadlineLocksNothing(t *testing.T) {
	r := newRig(t, tests.Reply{Hang: true})
	conn := r.conn("forced", "k1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	open := func() (stream.Client, error) { return nil, errors.New("no stream") }
	in := Input{
		Path: "/v1/chat/completions", Body: []byte(`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`),
		Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-deadline",
	}
	out := r.p.Handle(ctx, in, open)
	if out.Status != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504", out.Status)
	}
	for _, line := range r.log.Lines() {
		if strings.Contains(line, "recording a failed account failed") {
			t.Fatalf("a write was tried on the caller's dead context: %q", line)
		}
	}
	got, ok, err := r.st.Connections().Get(context.Background(), conn.ID)
	if err != nil || !ok {
		t.Fatalf("read the connection back: %v", err)
	}
	for _, key := range []string{repository.LockKey("fm"), repository.LockKey("")} {
		if got.Doc().Has(key) {
			t.Fatalf("a timed-out probe locked the account under %s", key)
		}
	}
}

// The router's own body budget ends while the caller is still waiting, so that
// timeout is the upstream's and still locks the account.
func TestATimeoutInsideTheCallersDeadlineStillLocks(t *testing.T) {
	r := newRig(t, tests.Reply{Hang: true})
	conn := r.conn("wire", "k1")
	r.p.d.BodyTimeout = 50 * time.Millisecond
	open := func() (stream.Client, error) { return nil, errors.New("no stream") }
	in := Input{
		Path: "/v1/chat/completions", Body: []byte(`{"model":"wire/short","messages":[{"role":"user","content":"hi"}]}`),
		Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-timeout",
	}
	out := r.p.Handle(context.Background(), in, open)
	if out.Status != http.StatusGatewayTimeout {
		t.Fatalf("status %d, want 504", out.Status)
	}
	got, ok, err := r.st.Connections().Get(context.Background(), conn.ID)
	if err != nil || !ok {
		t.Fatalf("read the connection back: %v", err)
	}
	if !got.Doc().Has(repository.LockKey("short")) {
		t.Fatal("a timeout the router owned left the account unlocked")
	}
}
