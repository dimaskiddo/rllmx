package pipeline

import (
	"context"
	"errors"
	"testing"

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
