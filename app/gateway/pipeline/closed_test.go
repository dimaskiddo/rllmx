package pipeline

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"rllmx/app/access/identity"
	"rllmx/app/llm/stream"
	"rllmx/app/llm/translate"
	"rllmx/tests"
)

// dropAfter is a client that takes n frames and then goes away.
type dropAfter struct{ left int }

func (d *dropAfter) Frame(translate.Frame) error {
	if d.left <= 0 {
		return errors.New("gone")
	}
	d.left--
	return nil
}

func (d *dropAfter) Raw([]byte) error { return nil }

func TestAStreamTheClientLeftAfterTokensFlowedStillSpendsARequest(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	in := Input{Path: "/v1/chat/completions", Body: []byte(`{"model":"forced/fm","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-9"}
	r.p.Handle(context.Background(), in, func() (stream.Client, error) { return &dropAfter{left: 3}, nil })
	row := r.last()
	if row.PromptTokens+row.CompletionTokens == 0 {
		t.Fatalf("the test never reached the usage: %+v", row)
	}
	if row.Status >= 400 {
		t.Fatalf("a request that used tokens is recorded at %d, which no budget counts", row.Status)
	}
	if row.ErrorClass != "client_closed" {
		t.Fatalf("the row lost the reason: %q", row.ErrorClass)
	}
}

func TestAClientThatLeftBeforeAnyTokenIsStillAnErrorRow(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	in := Input{Path: "/v1/chat/completions", Body: []byte(`{"model":"forced/fm","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-10"}
	r.p.Handle(context.Background(), in, func() (stream.Client, error) { return &dropAfter{left: 0}, nil })
	if row := r.last(); row.PromptTokens+row.CompletionTokens == 0 && row.Status < 400 {
		t.Fatalf("a request that never answered counts as served: %+v", row)
	}
}

// blockedPacer holds the attempt until the context ends, the way the shared
// queue does when it waits behind other connections.
type blockedPacer struct{}

func (blockedPacer) Wait(ctx context.Context, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestADeadlineIsATimeoutAndACancelIsAClosedClient(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		class  string
		ctx    func() (context.Context, context.CancelFunc)
	}{
		{"a probe or combo budget runs out", http.StatusGatewayTimeout, "timeout", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 20*time.Millisecond)
		}},
		{"the client leaves", 499, "client_closed", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(20*time.Millisecond, cancel)
			return ctx, cancel
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
			r.conn("forced", "k1")
			r.p.d.Pacer = blockedPacer{}
			ctx, cancel := tt.ctx()
			defer cancel()
			in := Input{Path: "/v1/chat/completions", Body: []byte(`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`),
				Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-11"}
			out := r.p.Handle(ctx, in, func() (stream.Client, error) { return nil, errors.New("never") })
			if out.Status != tt.status {
				t.Fatalf("status %d, want %d", out.Status, tt.status)
			}
			if row := r.last(); row.ErrorClass != tt.class {
				t.Fatalf("class %q, want %q", row.ErrorClass, tt.class)
			}
			if len(r.up.Calls()) != 0 {
				t.Fatal("a request whose context ended reached the provider")
			}
		})
	}
}
