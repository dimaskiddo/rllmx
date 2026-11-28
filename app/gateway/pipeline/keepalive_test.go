package pipeline

import (
	"context"
	"sync"
	"testing"
	"time"

	"rllmx/app/access/identity"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/tests"
)

// rawRecorder is a client that keeps what the stream wrote raw and counts the
// frames, so a test can wait for the stream to open.
type rawRecorder struct {
	mu     sync.Mutex
	raws   [][]byte
	frames int
}

func (r *rawRecorder) Frame(translate.Frame) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frames++
	return nil
}

func (r *rawRecorder) sawFrame() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.frames > 0
}

func (r *rawRecorder) Raw(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.raws = append(r.raws, append([]byte(nil), b...))
	return nil
}

func (r *rawRecorder) sawKeepalive() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, b := range r.raws {
		if string(b) == stream.KeepaliveFrame {
			return true
		}
	}
	return false
}

func openRecorded(t *testing.T, r *rig, path, body string, c *rawRecorder, ctx context.Context) chan Output {
	t.Helper()
	done := make(chan Output, 1)
	go func() {
		done <- r.p.Handle(ctx, Input{
			Path: path, Body: []byte(body), Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-ka",
		}, func() (stream.Client, error) { return c, nil })
	}()
	return done
}

// A client that times out on silence keeps the answer because the stream says
// something while the upstream is still thinking.
func TestAStreamedAnswerKeepsAnSseClientAlive(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()[:2], Pause: time.Millisecond, ThenSilent: true})
	r.conn("forced", "k1")
	r.p.d.Keepalive = 5 * time.Millisecond
	c := &rawRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := openRecorded(t, r, "/v1/chat/completions", `{"model":"forced/fm","stream":true,"messages":[{"role":"user","content":"hi"}]}`, c, ctx)
	deadline := time.Now().Add(2 * time.Second)
	for !c.sawKeepalive() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done
	if !c.sawKeepalive() {
		t.Fatal("a quiet open stream sent no keepalive")
	}
}

// A single-target stream opens on the first quiet tick: no other member can
// answer, so staying silent would only lose the client.
func TestASilentUpstreamLetsAnAloneStreamOpen(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Silent: true})
	r.conn("forced", "k1")
	r.p.d.Keepalive = 5 * time.Millisecond
	c := &rawRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := openRecorded(t, r, "/v1/chat/completions", `{"model":"forced/fm","stream":true,"messages":[{"role":"user","content":"hi"}]}`, c, ctx)
	deadline := time.Now().Add(2 * time.Second)
	for !c.sawKeepalive() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	<-done
	if !c.sawKeepalive() {
		t.Fatal("a stream with no failover left stayed silent")
	}
}

// While a later member can still answer, the stream stays closed: a comment
// here would commit the answer to the silent member.
func TestAComboKeepsTheStreamClosedWhileAMemberRemains(t *testing.T) {
	r := comboRig(t, []string{"forced/fm", "wire/short"},
		tests.Reply{ContentType: "text/event-stream", Silent: true},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	r.p.d.Keepalive = 5 * time.Millisecond
	c := &rawRecorder{}
	out := <-openRecorded(t, r, "/v1/chat/completions", `{"model":"chain","stream":true,"messages":[{"role":"user","content":"hi"}]}`, c, context.Background())
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if c.sawKeepalive() {
		t.Fatal("a combo opened the stream while a member could still answer")
	}
	if row := r.last(); row.Provider != "wire" || row.Model != "short" {
		t.Fatalf("the row does not name the member that answered: %+v", row)
	}
}

// While another account can still answer, the stream stays closed: a comment
// would commit the answer to the silent account and end its failover.
func TestASilentAccountWithAnotherLeftKeepsTheStreamClosed(t *testing.T) {
	r := newRig(t,
		tests.Reply{ContentType: "text/event-stream", Silent: true},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("forced", "k2")
	r.p.d.Keepalive = 5 * time.Millisecond
	c := &rawRecorder{}
	out := <-openRecorded(t, r, "/v1/chat/completions", `{"model":"forced/fm","stream":true,"messages":[{"role":"user","content":"hi"}]}`, c, context.Background())
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if c.sawKeepalive() {
		t.Fatal("the stream opened while another account could still answer")
	}
	if !c.sawFrame() {
		t.Fatal("the second account did not answer")
	}
	if calls := len(r.up.Calls()); calls != 2 {
		t.Fatalf("%d upstream calls, want 2", calls)
	}
}

// NDJSON has no comment line, so an Ollama answer must never get one.
func TestAnOllamaStreamGetsNoComment(t *testing.T) {
	// The upstream speaks OpenAI SSE; the client asked for Ollama's NDJSON.
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()[:2], ThenSilent: true})
	r.conn("forced", "k1")
	r.p.d.Keepalive = 5 * time.Millisecond
	c := &rawRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := openRecorded(t, r, "/v1/api/chat", `{"model":"forced/fm","stream":true,"messages":[{"role":"user","content":"hi"}]}`, c, ctx)
	deadline := time.Now().Add(2 * time.Second)
	for !c.sawFrame() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	// The upstream is now silent; an SSE client would be getting comments here.
	time.Sleep(25 * time.Millisecond)
	cancel()
	<-done
	if c.sawKeepalive() {
		t.Fatal("an Ollama stream got a comment line")
	}
}
