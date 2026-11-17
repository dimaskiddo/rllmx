package pipeline

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"rllmx/app/access/identity"
	"rllmx/app/llm/stream"
	"rllmx/tests"
)

// pinned sends one request with a connection pin, the way a probe does.
func (r *rig) pinned(connID, path, body string) Output {
	r.t.Helper()
	rec := httptest.NewRecorder()
	open := func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) }
	return r.p.Handle(context.Background(), Input{
		Path: path, Body: []byte(body), Caller: identity.Probe(), RequestID: "probe-1", Connection: connID, Cache: "bypass",
	}, open)
}

func TestAPinnedRequestUsesTheConnectionItNames(t *testing.T) {
	r := newRig(t,
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	second := r.conn(nodeID, "k2")
	out := r.pinned(second.ID, "/v1/chat/completions", chatBody)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if calls := len(r.up.Calls()); calls != 1 {
		t.Fatalf("%d upstream calls, want one", calls)
	}
	if row := r.last(); row.ConnectionID != second.ID {
		t.Fatalf("%+v", row)
	}
}

func TestAPinnedRequestNeverSpendsOnAnotherAccount(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	before := len(r.rows.All())
	out := r.pinned("absent", "/v1/chat/completions", chatBody)
	if out.Status != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if calls := len(r.up.Calls()); calls != 0 {
		t.Fatalf("%d upstream calls, want none", calls)
	}
	if n := len(r.rows.All()) - before; n != 1 {
		t.Fatalf("%d usage rows, want one", n)
	}
}

func TestAComboCannotBePinned(t *testing.T) {
	r := comboRig(t, []string{"forced/fm"}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	out := r.pinned("c1", "/v1/chat/completions", comboAsk)
	if out.Status != http.StatusBadRequest {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if len(r.up.Calls()) != 0 {
		t.Fatal("a refused pin reached the upstream")
	}
}
