package translate

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// openAIReader reads Chat Completions chunks.
type openAIReader struct {
	started bool
	// seen is the tool-call indexes already opened and their ids.
	seen  map[int]string
	calls int
}

// NewStreamReader returns the reader for Chat Completions streams.
func (OpenAICodec) NewStreamReader(c *Ctx) StreamReader {
	return &openAIReader{seen: map[int]string{}}
}

func (r *openAIReader) Flush() []Chunk { return nil }

func (r *openAIReader) Read(_ string, data []byte) ([]Chunk, error) {
	if !gjson.ValidBytes(data) {
		return nil, nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil, nil
	}
	if e := g.Get("error"); e.Exists() {
		return []Chunk{{Err: readErrorObject(e)}}, nil
	}
	ch := Chunk{ID: g.Get("id").String(), Model: g.Get("model").String(), Created: g.Get("created").Int()}
	if u := g.Get("usage"); u.IsObject() && u.Get("prompt_tokens").Exists() {
		us := readOpenAIUsage(u)
		ch.Usage = &us
	}
	choice := g.Get("choices.0")
	if choice.Exists() {
		d := choice.Get("delta")
		ch.Text = d.Get("content").String()
		ch.Reasoning = streamReasoning(d)
		for _, tc := range d.Get("tool_calls").Array() {
			ch.ToolCalls = append(ch.ToolCalls, r.toolCall(tc))
		}
		if f := choice.Get("finish_reason").String(); f != "" {
			ch.FinishReason = FinishWithCalls(finishFromOpenAI(f), r.calls)
		}
	}
	if ch.Text == "" && ch.Reasoning == "" && len(ch.ToolCalls) == 0 && ch.FinishReason == "" && ch.Usage == nil && r.started {
		return nil, nil
	}
	if !r.started {
		r.started = true
		ch.Role = RoleAssistant
	}
	return []Chunk{ch}, nil
}

// toolCall reads one fragment. A fragment that opens an index without an id
// gets one, because strict clients abort a call that has none.
func (r *openAIReader) toolCall(tc gjson.Result) ToolCallDelta {
	idx := int(tc.Get("index").Int())
	d := ToolCallDelta{
		Index: idx, ID: tc.Get("id").String(), Name: tc.Get("function.name").String(),
		Arguments: tc.Get("function.arguments").String(),
	}
	if _, open := r.seen[idx]; !open {
		if d.ID == "" {
			d.ID = fmt.Sprintf("call_stream_%d", idx)
		}
		r.seen[idx] = d.ID
		r.calls++
	} else if d.ID == "" || d.ID == r.seen[idx] {
		d.ID = ""
	}
	return d
}

// streamReasoning reads reasoning under the names providers use for it.
func streamReasoning(delta gjson.Result) string {
	for _, k := range []string{"reasoning_content", "reasoning"} {
		if v := delta.Get(k); v.Type == gjson.String && v.String() != "" {
			return v.String()
		}
	}
	var b strings.Builder
	for _, d := range delta.Get("reasoning_details").Array() {
		b.WriteString(d.Get("text").String())
	}
	return b.String()
}

func readErrorObject(e gjson.Result) *ChunkError {
	if e.Type == gjson.String {
		return &ChunkError{Message: e.String()}
	}
	return &ChunkError{Type: e.Get("type").String(), Message: e.Get("message").String()}
}

func finishFromOpenAI(f string) string {
	switch f {
	case FinishLength, FinishToolCalls, FinishFilter:
		return f
	case "function_call":
		return FinishToolCalls
	}
	return FinishStop
}

// openAIWriter writes Chat Completions chunks.
type openAIWriter struct {
	g       gate
	started bool
	roled   bool
	id      string
	model   string
	created int64
	calls   int
	opened  map[int]bool
	closed  bool
}

// NewStreamWriter returns the writer for Chat Completions streams.
func (OpenAICodec) NewStreamWriter(c *Ctx) StreamWriter {
	return &openAIWriter{opened: map[int]bool{}}
}

func (w *openAIWriter) Held() bool { return w.g.held() }

// streamID returns an id a strict client accepts: a short or generic one is
// replaced by a random one that stays for the rest of the stream.
func (w *openAIWriter) streamID(in string) string {
	if w.id != "" {
		return w.id
	}
	if in == "" || in == "chat" || in == "completion" || len(in) < 8 {
		var b [8]byte
		_, _ = rand.Read(b[:])
		in = "chatcmpl-" + hex.EncodeToString(b[:])
	}
	w.id = in
	return in
}

func (w *openAIWriter) frame(delta obj, finish any, usage *Usage) Frame {
	choice := obj{"index": 0, "delta": delta, "finish_reason": finish}
	o := obj{"id": w.id, "object": "chat.completion.chunk", "created": w.created, "model": w.model, "choices": []any{choice}}
	if usage != nil {
		o["usage"] = openAIUsage(*usage)
	}
	b, _ := json.Marshal(o)
	return Frame{Data: b}
}

func (w *openAIWriter) Write(c Chunk) ([]Frame, error) {
	if c.Err != nil {
		return nil, nil
	}
	w.streamID(c.ID)
	if w.model == "" {
		w.model = c.Model
	}
	if w.created == 0 {
		w.created = c.Created
		if w.created == 0 {
			w.created = time.Now().Unix()
		}
	}
	var out []Frame
	delta := obj{}
	if !w.roled {
		delta["role"] = "assistant"
		w.roled = true
	}
	if c.Text != "" {
		delta["content"] = c.Text
	}
	if c.Reasoning != "" {
		delta["reasoning_content"] = c.Reasoning
	}
	if len(c.ToolCalls) > 0 {
		var calls []any
		for _, tc := range c.ToolCalls {
			fn := obj{"arguments": tc.Arguments}
			call := obj{"index": tc.Index}
			if !w.opened[tc.Index] {
				w.opened[tc.Index] = true
				w.calls++
				call["id"], call["type"] = tc.ID, "function"
				fn["name"] = tc.Name
			}
			call["function"] = fn
			calls = append(calls, call)
		}
		delta["tool_calls"] = calls
	}
	if len(delta) > 0 {
		w.started = true
		out = append(out, w.frame(delta, nil, nil))
	}
	if w.g.see(c) {
		out = append(out, w.finishFrame())
	}
	return out, nil
}

func (w *openAIWriter) finishFrame() Frame {
	u := w.g.usage
	return w.frame(obj{}, FinishWithCalls(w.g.finish, w.calls), &u)
}

func (w *openAIWriter) Close() []Frame {
	if !w.started || w.closed {
		return nil
	}
	w.closed = true
	var out []Frame
	if !w.g.released {
		if !w.g.hasFin {
			w.g.finish, w.g.hasFin = FinishStop, true
		}
		w.g.released = true
		f := w.finishFrame()
		if !w.g.hasUsage {
			f = w.frame(obj{}, FinishWithCalls(w.g.finish, w.calls), nil)
		}
		out = append(out, f)
	}
	return append(out, Frame{Data: []byte("[DONE]"), Done: true})
}
