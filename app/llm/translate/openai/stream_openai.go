package openai

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"rllmx/app/llm/translate"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
func (OpenAICodec) NewStreamReader(c *translate.Ctx) translate.StreamReader {
	return &openAIReader{seen: map[int]string{}}
}

func (r *openAIReader) Flush() []translate.Chunk { return nil }

func (r *openAIReader) Read(_ string, data []byte) []translate.Chunk {
	if !gjson.ValidBytes(data) {
		return nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil
	}
	if e := g.Get("error"); e.Exists() {
		return []translate.Chunk{{Err: ReadErrorObject(e)}}
	}
	ch := translate.Chunk{ID: g.Get("id").String(), Model: g.Get("model").String(), Created: g.Get("created").Int()}
	if u := g.Get("usage"); u.IsObject() && u.Get("prompt_tokens").Exists() {
		us := translate.ReadOpenAIUsage(u)
		ch.Usage = &us
	}
	choice := g.Get("choices.0")
	if choice.Exists() {
		d := choice.Get("delta")
		ch.Text, ch.Reasoning = streamContent(d)
		if r := StreamReasoning(d); r != "" {
			ch.Reasoning = r
		}
		for _, tc := range d.Get("tool_calls").Array() {
			ch.ToolCalls = append(ch.ToolCalls, r.toolCall(tc))
		}
		if f := choice.Get("finish_reason").String(); f != "" {
			ch.FinishReason = translate.FinishWithCalls(FinishFromOpenAI(f), r.calls)
		}
	}
	if ch.Text == "" && ch.Reasoning == "" && len(ch.ToolCalls) == 0 && ch.FinishReason == "" && ch.Usage == nil && r.started {
		return nil
	}
	if !r.started {
		r.started = true
		ch.Role = translate.RoleAssistant
	}
	return []translate.Chunk{ch}
}

// toolCall reads one fragment. A fragment that opens an index without an id
// gets one, because strict clients abort a call that has none.
func (r *openAIReader) toolCall(tc gjson.Result) translate.ToolCallDelta {
	idx := int(tc.Get("index").Int())
	d := translate.ToolCallDelta{
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

// StreamReasoning reads reasoning under the names providers use for it.
func StreamReasoning(delta gjson.Result) string {
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

// ReadErrorObject reads an error a stream carried: either a bare message or an
// object with a type and a message.
func ReadErrorObject(e gjson.Result) *translate.ChunkError {
	if e.Type == gjson.String {
		return &translate.ChunkError{Message: e.String()}
	}
	return &translate.ChunkError{Type: e.Get("type").String(), Message: e.Get("message").String()}
}

func FinishFromOpenAI(f string) string {
	switch f {
	case translate.FinishLength, translate.FinishToolCalls, translate.FinishFilter:
		return f
	case "function_call":
		return translate.FinishToolCalls
	}
	return translate.FinishStop
}

// openAIWriter writes Chat Completions chunks.
type openAIWriter struct {
	g       translate.Gate
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
func (OpenAICodec) NewStreamWriter(c *translate.Ctx) translate.StreamWriter {
	return &openAIWriter{opened: map[int]bool{}}
}

func (w *openAIWriter) Held() bool { return w.g.Held() }

// streamID returns an id a strict client accepts: a short or generic one is
// replaced by a random one that stays for the rest of the stream.
func (w *openAIWriter) streamID(in string) string {
	if w.id != "" {
		return w.id
	}
	w.id = StrictStreamID(in)
	return w.id
}

// WeakStreamID reports an id a strict client would refuse: empty, a generic
// word or shorter than eight characters.
func WeakStreamID(id string) bool {
	return id == "" || id == "chat" || id == "completion" || len(id) < 8
}

// StrictStreamID returns in when a strict client accepts it, and a fresh random
// chatcmpl id otherwise.
func StrictStreamID(in string) string {
	if !WeakStreamID(in) {
		return in
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "chatcmpl-" + hex.EncodeToString(b[:])
}

func (w *openAIWriter) frame(delta translate.Obj, finish any, usage *translate.Usage) translate.Frame {
	choice := translate.Obj{"index": 0, "delta": delta, "finish_reason": finish}
	o := translate.Obj{"id": w.id, "object": "chat.completion.chunk", "created": w.created, "model": w.model, "choices": []any{choice}}
	if usage != nil {
		o["usage"] = openAIUsage(*usage)
	}
	return translate.Frame{Data: translate.FrameData(o)}
}

// textFrame is frame for the delta almost every chunk carries: an optional
// role, text and reasoning, no tool call and no finish. It writes the bytes in
// the key order Obj gives (sorted), so the output is the same as frame's, with
// one buffer and no maps.
func (w *openAIWriter) textFrame(role bool, text, reasoning string) translate.Frame {
	b := make([]byte, 0, 160+len(text)+len(reasoning)+len(w.id)+len(w.model))
	b = append(b, `{"choices":[{"delta":{`...)
	comma := false
	put := func(key, val string) {
		if comma {
			b = append(b, ',')
		}
		comma = true
		b = append(b, '"')
		b = append(b, key...)
		b = append(b, `":`...)
		b = appendJSONString(b, val)
	}
	// Sorted: content, reasoning_content, role.
	if text != "" {
		put("content", text)
	}
	if reasoning != "" {
		put("reasoning_content", reasoning)
	}
	if role {
		put("role", "assistant")
	}
	b = append(b, `},"finish_reason":null,"index":0}],"created":`...)
	b = strconv.AppendInt(b, w.created, 10)
	b = append(b, `,"id":`...)
	b = appendJSONString(b, w.id)
	b = append(b, `,"model":`...)
	b = appendJSONString(b, w.model)
	b = append(b, `,"object":"chat.completion.chunk"}`...)
	return translate.Frame{Data: b}
}

// appendJSONString appends s as a JSON string exactly as encoding/json writes
// it: <, > and & as \u00XX, U+2028 and U+2029 escaped, invalid UTF-8 replaced by
// U+FFFD. A fuzz test holds it to json.Marshal.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '\\', '"':
				b = append(b, '\\', c)
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', 'f', 'f', 'f', 'd')
			i += size
			start = i
			continue
		}
		if r == ' ' || r == ' ' {
			b = append(b, s[start:i]...)
			b = append(b, `\u202`...)
			b = append(b, hex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

func (w *openAIWriter) Write(c translate.Chunk) []translate.Frame {
	if c.Err != nil {
		return nil
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
	var out []translate.Frame
	if len(c.ToolCalls) == 0 && (c.Text != "" || c.Reasoning != "") {
		role := !w.roled
		w.roled = true
		w.started = true
		out = append(out, w.textFrame(role, c.Text, c.Reasoning))
		if w.g.See(c) {
			out = append(out, w.finishFrame())
		}
		return out
	}
	delta := translate.Obj{}
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
			fn := translate.Obj{"arguments": tc.Arguments}
			call := translate.Obj{"index": tc.Index}
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
	if w.g.See(c) {
		out = append(out, w.finishFrame())
	}
	return out
}

func (w *openAIWriter) finishFrame() translate.Frame {
	u := w.g.Usage
	return w.frame(translate.Obj{}, translate.FinishWithCalls(w.g.Finish, w.calls), &u)
}

func (w *openAIWriter) Close() []translate.Frame {
	if !w.started || w.closed {
		return nil
	}
	w.closed = true
	var out []translate.Frame
	if !w.g.Released {
		if !w.g.HasFinish {
			w.g.Finish, w.g.HasFinish = translate.FinishStop, true
		}
		w.g.Released = true
		f := w.finishFrame()
		if !w.g.HasUsage {
			f = w.frame(translate.Obj{}, translate.FinishWithCalls(w.g.Finish, w.calls), nil)
		}
		out = append(out, f)
	}
	return append(out, translate.Frame{Data: []byte("[DONE]"), Done: true})
}

// streamContent reads a delta's content: a string as it is, or a list of parts
// as the visible text and the thinking a provider such as Mistral streams
// beside it. A part of any other kind carries neither.
func streamContent(d gjson.Result) (text, thinking string) {
	c := d.Get("content")
	if !c.IsArray() {
		return c.String(), ""
	}
	var t, k strings.Builder
	for _, part := range c.Array() {
		switch part.Get("type").String() {
		case "text":
			t.WriteString(part.Get("text").String())
		case "thinking":
			if inner := part.Get("thinking"); inner.IsArray() {
				for _, p := range inner.Array() {
					k.WriteString(p.Get("text").String())
				}
			} else {
				k.WriteString(inner.String())
			}
		}
	}
	return t.String(), k.String()
}
