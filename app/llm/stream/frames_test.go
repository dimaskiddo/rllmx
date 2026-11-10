package stream

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"rllmx/app/protocol/eventstream"
)

// frame wraps one payload the way each bedrock wire does.
func frame(w FramesWire, name, payload string) []byte {
	headers := map[string]string{":message-type": "event", ":event-type": "chunk"}
	if w == FramesKiro {
		if name != "" {
			headers[":event-type"] = name
		}
		return eventstream.Encode(headers, []byte(payload))
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))
	return eventstream.Encode(headers, []byte(`{"bytes":"`+encoded+`"}`))
}

func events(t *testing.T, w FramesWire, body []byte) []Event {
	t.Helper()
	src := NewFrames(bytes.NewReader(body), w)
	var out []Event
	for range 8 {
		ev, err := src.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func TestClaudeFramesAreNamedByTheirOwnType(t *testing.T) {
	body := append(frame(FramesBedrockClaude, "", `{"type":"message_start"}`),
		frame(FramesBedrockClaude, "", `{"delta":{"text":"hi"},"type":"content_block_delta"}`)...)

	got := events(t, FramesBedrockClaude, body)
	if len(got) != 2 {
		t.Fatalf("%d events, want 2", len(got))
	}
	if got[0].Name != "message_start" || got[1].Name != "content_block_delta" {
		t.Errorf("names %q and %q", got[0].Name, got[1].Name)
	}
	if string(got[1].Data) != `{"delta":{"text":"hi"},"type":"content_block_delta"}` {
		t.Errorf("payload %q", got[1].Data)
	}
}

func TestOpenAIFramesCarryNoEventName(t *testing.T) {
	got := events(t, FramesBedrockOpenAI, frame(FramesBedrockOpenAI, "", `{"choices":[]}`))
	if len(got) != 1 || got[0].Name != "" {
		t.Fatalf("events %+v", got)
	}
}

func TestKiroFramesAreNamedByTheHeader(t *testing.T) {
	body := append(frame(FramesKiro, "assistantResponseEvent", `{"content":"hi"}`),
		frame(FramesKiro, "messageStopEvent", `{"stopReason":"end_turn"}`)...)

	got := events(t, FramesKiro, body)
	if len(got) != 2 {
		t.Fatalf("%d events, want 2", len(got))
	}
	if got[0].Name != "assistantResponseEvent" || string(got[0].Data) != `{"content":"hi"}` {
		t.Errorf("first %+v", got[0])
	}
}

func TestAnInBandFailureBecomesAnErrorEvent(t *testing.T) {
	body := eventstream.Encode(map[string]string{":message-type": "exception", ":exception-type": "ThrottlingException"},
		[]byte(`{"message":"slow down"}`))

	got := events(t, FramesKiro, body)
	if len(got) != 1 || got[0].Name != "error" {
		t.Fatalf("events %+v", got)
	}
	if !bytes.Contains(got[0].Data, []byte(`"message":"slow down"`)) || !bytes.Contains(got[0].Data, []byte(`"type":"ThrottlingException"`)) {
		t.Errorf("failure %s", got[0].Data)
	}
}

func TestABrokenFrameIsAnErrorNotAnEnd(t *testing.T) {
	good := frame(FramesKiro, "messageStopEvent", `{}`)
	good[len(good)-1] ^= 0xff

	if _, err := NewFrames(bytes.NewReader(good), FramesKiro).Next(); !errors.Is(err, eventstream.ErrMessage) {
		t.Errorf("got %v, want the frame's own failure", err)
	}
}

func TestAChunkWithNoBytesIsRefused(t *testing.T) {
	body := eventstream.Encode(map[string]string{":event-type": "chunk", ":message-type": "event"}, []byte(`{}`))
	if _, err := NewFrames(bytes.NewReader(body), FramesBedrockClaude).Next(); !errors.Is(err, ErrBadFrame) {
		t.Errorf("got %v, want ErrBadFrame", err)
	}
}

func TestAnEventStreamBodyIsClassified(t *testing.T) {
	for name, want := range map[string]Kind{
		"application/vnd.amazon.eventstream": KindEvents,
		"application/json":                   KindJSON,
		"text/event-stream":                  KindSSE,
		"text/html; charset=utf-8":           KindOther,
	} {
		if got := KindOf(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
}
