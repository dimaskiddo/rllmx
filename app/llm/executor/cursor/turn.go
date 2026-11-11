package cursor

import (
	"encoding/json"
	"errors"
	"io"
	"sync"

	"google.golang.org/protobuf/encoding/protowire"
)

// errTurn means the service's answer carried a message this router cannot read.
var errTurn = errors.New("cursor: the answer holds an unknown message")

// unavailableTool is what a call the service made and this router cannot run is
// answered with, so the model turns to what it has instead of waiting.
const unavailableTool = "Tool not available in this environment. Use the tools provided instead."

// turnRead is the answer as a pull reader: every Read decodes what arrived,
// answers what the service asked for, and hands back the chat chunks that came
// out. Nothing runs in the background, so a reply the service waits on goes out
// from the goroutine that already read the request.
type turnRead struct {
	frames *frameReader
	write  func([]byte) error
	close  func() error

	model string

	mu      sync.Mutex
	pending []chunk
	buf     []byte
	done    bool
	finish  string
	err     error
	// thinking collects a composer model's answer, which arrives inside its
	// thinking text.
	thinking    string
	composer    bool
	emittedText bool
}

// chunk is one delta this router emits.
type chunk struct {
	text     string
	thinking string
	call     *toolCall
}

// toolCall is a call the model asked for.
type toolCall struct {
	ID        string
	Name      string
	Arguments string
}

// Read fills p from the chunks the answer produced.
func (t *turnRead) Read(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for len(t.buf) == 0 {
		if t.err != nil {
			return 0, t.err
		}
		// A delta that arrived while the turn was ending belongs to it, so the
		// closing frame waits until every frame before it is handed over.
		if t.done && len(t.pending) == 0 {
			return 0, io.EOF
		}
		if !t.done {
			if err := t.step(); err != nil {
				t.err = err
				return 0, err
			}
		}
		t.buf = encodeChunks(t.model, t.pending, t.finish)
		t.pending = nil
		t.finish = ""
	}
	n := copy(p, t.buf)
	t.buf = t.buf[n:]
	return n, nil
}

// Close ends the exchange and releases the stream.
func (t *turnRead) Close() error {
	if t.close != nil {
		return t.close()
	}
	return nil
}

// step reads one frame and turns it into chunks.
func (t *turnRead) step() error {
	f, err := t.frames.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			t.endTurn("")
			return nil
		}
		return err
	}
	if f.trailer {
		return t.status(f.payload)
	}
	return t.server(f.payload)
}

// status reads the end-of-stream trailer. The reference drops it, so a refusal
// arrives there as an empty turn; here it is the refusal it was.
func (t *turnRead) status(payload []byte) error {
	var body struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &body) != nil || body.Error == nil || body.Error.Code == "" {
		return nil
	}
	t.done = true
	return &statusError{code: body.Error.Code}
}

// server reads one AgentServerMessage.
func (t *turnRead) server(payload []byte) error {
	if update, ok := readMessage(payload, serverInteraction); ok {
		return t.update(update)
	}
	if exec, ok := readMessage(payload, serverExec); ok {
		return t.exec(exec)
	}
	if kv, ok := readMessage(payload, serverKV); ok {
		return t.kv(kv)
	}
	return nil
}

// update reads one interaction update: a text delta, a thinking delta, or the
// turn's end.
func (t *turnRead) update(body []byte) error {
	if delta, ok := readMessage(body, updateText); ok {
		if s := readText(delta, updateText); s != "" {
			t.emittedText = true
			t.pending = append(t.pending, chunk{text: s})
		}
	}
	if delta, ok := readMessage(body, updateThinking); ok {
		if s := readText(delta, updateText); s != "" {
			t.thinking += s
			if t.composer {
				t.composerText()
			}
		}
	}
	if has(body, updateEnd) {
		t.endTurn("")
	}
	return nil
}

// composerText hands over the visible part of a composer model's thinking: the
// answer follows the closing think marker, and the service sends no text delta
// for it.
func (t *turnRead) composerText() {
	i := lastIndex(t.thinking, composerEnd)
	if i < 0 {
		return
	}
	visible := t.thinking[i+len(composerEnd):]
	if visible == "" {
		return
	}
	t.thinking = t.thinking[:i]
	t.emittedText = true
	t.pending = append(t.pending, chunk{text: visible})
}

// composerEnd closes a composer model's thinking text.
const composerEnd = "</think>"

// exec answers a request the service made of this router. A context request is
// acknowledged, a tool probe is refused, and a call to an offered tool ends the
// turn with that call.
func (t *turnRead) exec(body []byte) error {
	id, _ := readNumber(body, execID)
	ref := readText(body, execRef)
	if has(body, execContext) {
		return t.write(clientExec(id, ref, execResultCtx, message(1, nil)))
	}
	if call, ok := readMessage(body, execMCPCall); ok {
		name := readText(call, mcpArgsToolName)
		if name == "" {
			name = readText(call, mcpArgsName)
		}
		if name == "" {
			return errTurn
		}
		args, _ := json.Marshal(decodeArguments(call))
		t.pending = append(t.pending, chunk{call: &toolCall{
			ID: firstText(readText(call, mcpArgsCallID), newMessageID()), Name: name, Arguments: string(args),
		}})
		t.endTurn("tool_calls")
		return nil
	}
	// An IDE tool this router has none of. Refusing it lets the model answer or
	// call an offered tool instead of holding the stream open.
	variant := otherField(body, execContext, execMCPCall)
	if variant == 0 {
		return nil
	}
	return t.write(clientExec(id, ref, variant, message(2, text(paramValue, unavailableTool))))
}

// decodeArguments reads a call's arguments, a map of values.
func decodeArguments(call []byte) map[string]any {
	out := map[string]any{}
	for len(call) > 0 {
		num, kind, n := protowire.ConsumeTag(call)
		if n < 0 {
			return out
		}
		call = call[n:]
		if num != mcpArgsEntry || kind != protowire.BytesType {
			m := protowire.ConsumeFieldValue(num, kind, call)
			if m < 0 {
				return out
			}
			call = call[m:]
			continue
		}
		entry, m := protowire.ConsumeBytes(call)
		if m < 0 {
			return out
		}
		call = call[m:]
		if value, ok := readMessage(entry, mapValue); ok {
			out[readText(entry, mapKey)] = decodeValue(value)
		}
	}
	return out
}

// kv answers the service's own storage call, which holds the stream open until
// it is told the value is there.
func (t *turnRead) kv(body []byte) error {
	id, _ := readNumber(body, kvID)
	metadata, _ := readMessage(body, kvMetadata)
	switch {
	case has(body, kvGet):
		return t.write(clientKV(id, kvGet, message(1, nil), metadata))
	case has(body, kvSet):
		return t.write(clientKV(id, kvSet, nil, metadata))
	}
	return nil
}

// endTurn closes the answer once its deltas are handed over. A model whose
// thinking carried the answer gives it as the text when nothing else came out.
func (t *turnRead) endTurn(reason string) {
	if !t.emittedText && t.thinking != "" {
		t.pending = append(t.pending, chunk{text: t.thinking})
	}
	if reason == "" {
		reason = "stop"
	}
	t.done, t.finish = true, reason
}
