package pipeline

import (
	"bytes"
	"context"
	"io"
	"testing"

	"rllmx/app/eventstream"
	"rllmx/app/stream"
	"rllmx/app/translate"
)

// TestAFramedAnswerIsCollectedAsThoughItWereSSE holds the seam the AWS
// providers depend on: a binary body is read through the same pump every other
// upstream uses, and the executor does no conversion of its own.
func TestAFramedAnswerIsCollectedAsThoughItWereSSE(t *testing.T) {
	var body bytes.Buffer
	body.Write(eventstream.Encode(map[string]string{":event-type": "assistantResponseEvent", ":message-type": "event"}, []byte(`{"content":"hi"}`)))
	body.Write(eventstream.Encode(map[string]string{":event-type": "messageStopEvent", ":message-type": "event"}, []byte(`{"stopReason":"end_turn"}`)))

	if got := stream.KindOf("application/vnd.amazon.eventstream"); got != stream.KindEvents {
		t.Fatalf("kind %v, want KindEvents", got)
	}
	got, err := stream.Collect(context.Background(), io.NopCloser(&body),
		stream.NewFrames(&body, stream.FramesKiro), translate.FormatKiro,
		translate.NewCtx(translate.FormatKiro, translate.FormatOpenAI), stream.Timers{}, 0)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got.Response.Message.Text() != "hi" {
		t.Errorf("text %q", got.Response.Message.Text())
	}
	if got.Finish != translate.FinishStop {
		t.Errorf("finish %q", got.Finish)
	}
}
