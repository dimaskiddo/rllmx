package stream

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"github.com/tidwall/gjson"

	"rllmx/app/protocol/eventstream"
)

// ErrBadFrame means a frame of an upstream's binary stream could not be read:
// a chunk with no payload, or a payload that is not what its wire promises.
var ErrBadFrame = errors.New("stream: the upstream sent an unreadable frame")

// FramesWire is what the payloads of one binary stream carry.
type FramesWire int

const (
	// FramesBedrockClaude is Bedrock's Anthropic endpoint: each chunk is a base64
	// Anthropic event under "bytes", named by its own "type".
	FramesBedrockClaude FramesWire = iota
	// FramesBedrockOpenAI is Bedrock serving a Chat Completions model: the same
	// envelope around a chat chunk, which carries no event name.
	FramesBedrockOpenAI
	// FramesKiro is CodeWhisperer: the payload is the event itself, named by the
	// frame's ":event-type" header.
	FramesKiro
)

// Frames reads an AWS EventStream body and yields one event per frame, so a
// binary-framed answer reaches the rest of the pipeline as the events every
// other upstream sends.
type Frames struct {
	r    *eventstream.Reader
	wire FramesWire
}

// NewFrames reads a binary-framed upstream body as events.
func NewFrames(r io.Reader, w FramesWire) *Frames {
	return &Frames{r: eventstream.NewReader(r), wire: w}
}

// Next returns the next event, or io.EOF at the end of the stream. A framing
// failure is returned as it is: a truncated answer is never passed on as a
// shorter one.
func (f *Frames) Next() (Event, error) {
	for {
		fr, err := f.r.Next()
		if err != nil {
			return Event{}, err
		}
		if name := fr.Type(":message-type"); name == "exception" || name == "error" {
			return f.failure(fr)
		}
		data, name, err := f.payload(fr)
		if err != nil {
			return Event{}, err
		}
		if data == nil {
			continue
		}
		return Event{Name: name, Data: data}, nil
	}
}

// payload reads the event a frame carries: the bytes it wraps for Bedrock, or
// the payload itself for Kiro. nil means the frame carries no event.
func (f *Frames) payload(fr eventstream.Frame) (data []byte, name string, err error) {
	if f.wire == FramesKiro {
		if len(fr.Payload) == 0 {
			return nil, "", nil
		}
		return fr.Payload, fr.Type(":event-type"), nil
	}
	encoded := gjson.GetBytes(fr.Payload, "bytes").String()
	if encoded == "" {
		return nil, "", ErrBadFrame
	}
	inner, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, "", ErrBadFrame
	}
	if f.wire == FramesBedrockClaude {
		name = gjson.GetBytes(inner, "type").String()
		if name == "" {
			return nil, "", ErrBadFrame
		}
	}
	return inner, name, nil
}

// failure turns an in-band error frame into the event a reader already knows:
// an error event whose message is the provider's own.
func (f *Frames) failure(fr eventstream.Frame) (Event, error) {
	kind := fr.Type(":exception-type")
	if kind == "" {
		kind = fr.Type(":error-code")
	}
	msg := gjson.GetBytes(fr.Payload, "message").String()
	if msg == "" {
		msg = gjson.GetBytes(fr.Payload, "Message").String()
	}
	if msg == "" {
		msg = fr.Type(":error-message")
	}
	if msg == "" {
		msg = "the upstream reported a failure inside its stream"
	}
	if kind == "" {
		kind = "api_error"
	}
	out, err := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": kind}})
	if err != nil {
		return Event{}, ErrBadFrame
	}
	return Event{Name: "error", Data: out}, nil
}
