package translate

import (
	"fmt"
	"time"

	"github.com/tidwall/gjson"
)

// ollamaReader reads /api/chat lines.
type ollamaReader struct {
	started bool
	calls   int
	done    bool
}

// NewStreamReader returns the reader for Ollama streams.
func (OllamaCodec) NewStreamReader(c *Ctx) StreamReader {
	return &ollamaReader{}
}

func (r *ollamaReader) Flush() []Chunk { return nil }

func (r *ollamaReader) Read(_ string, data []byte) []Chunk {
	if !gjson.ValidBytes(data) {
		return nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil
	}
	if e := g.Get("error"); e.Exists() && !g.Get("message").Exists() {
		return []Chunk{{Err: readErrorObject(e)}}
	}
	ch := Chunk{Model: g.Get("model").String()}
	ch.Text = g.Get("message.content").String()
	ch.Reasoning = g.Get("message.thinking").String()
	for _, tc := range g.Get("message.tool_calls").Array() {
		args := tc.Get("function.arguments").Raw
		if args == "" {
			args = "{}"
		}
		idx := r.calls
		ch.ToolCalls = append(ch.ToolCalls, ToolCallDelta{Index: idx, ID: fmt.Sprintf("call_%d", idx), Name: tc.Get("function.name").String(), Arguments: args})
		r.calls++
	}
	if g.Get("done").Bool() && !r.done {
		r.done = true
		reason := FinishStop
		switch g.Get("done_reason").String() {
		case "length", "max_tokens":
			reason = FinishLength
		case "tool_calls", "tool_use":
			reason = FinishToolCalls
		}
		ch.FinishReason = FinishWithCalls(reason, r.calls)
		us := NormalizeUsage(Usage{Prompt: g.Get("prompt_eval_count").Int(), Completion: g.Get("eval_count").Int(), Cached: g.Get("prompt_eval_cached_count").Int()})
		ch.Usage = &us
	}
	if !r.started {
		r.started = true
		ch.Role = RoleAssistant
	}
	if ch.Text == "" && ch.Reasoning == "" && len(ch.ToolCalls) == 0 && ch.FinishReason == "" && ch.Role == "" {
		return nil
	}
	return []Chunk{ch}
}

// ollamaWriter writes /api/chat lines. Exactly one line has done:true.
type ollamaWriter struct {
	g       gate
	started bool
	model   string
	calls   []ToolCallDelta
}

// NewStreamWriter returns the writer for Ollama streams.
func (OllamaCodec) NewStreamWriter(c *Ctx) StreamWriter {
	return &ollamaWriter{}
}

func (w *ollamaWriter) Held() bool { return w.g.held() }

func (w *ollamaWriter) line(msg obj, extra obj) Frame {
	o := obj{"model": w.model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "message": msg}
	for k, v := range extra {
		o[k] = v
	}
	return Frame{Data: frameData(o)}
}

func (w *ollamaWriter) Write(c Chunk) []Frame {
	if c.Err != nil {
		return nil
	}
	if !w.started {
		w.started = true
		w.model = c.Model
	}
	var out []Frame
	for _, tc := range c.ToolCalls {
		w.hold(tc)
	}
	if c.Text != "" || c.Reasoning != "" {
		msg := obj{"role": "assistant", "content": c.Text}
		if c.Reasoning != "" {
			msg["thinking"] = c.Reasoning
		}
		out = append(out, w.line(msg, obj{"done": false}))
	}
	if w.g.see(c) {
		out = append(out, w.release()...)
	}
	return out
}

// hold collects the pieces of a call; an Ollama line carries it whole.
func (w *ollamaWriter) hold(tc ToolCallDelta) {
	for i, have := range w.calls {
		if have.Index == tc.Index {
			w.calls[i].Arguments += tc.Arguments
			return
		}
	}
	w.calls = append(w.calls, tc)
}

func (w *ollamaWriter) release() []Frame {
	var out []Frame
	if len(w.calls) > 0 {
		var calls []any
		for i, tc := range w.calls {
			calls = append(calls, obj{"function": obj{"index": i, "name": tc.Name, "arguments": rawOrEmpty(tc.Arguments)}})
		}
		w.calls = nil
		out = append(out, w.line(obj{"role": "assistant", "content": "", "tool_calls": calls}, obj{"done": false}))
	}
	reason := "stop"
	if w.g.finish == FinishLength {
		reason = "length"
	}
	u := w.g.usage
	return append(out, w.line(obj{"role": "assistant", "content": ""}, obj{
		"done": true, "done_reason": reason, "prompt_eval_count": u.Prompt, "eval_count": u.Completion,
	}))
}

func (w *ollamaWriter) Close() []Frame {
	if !w.started || w.g.released {
		return nil
	}
	if !w.g.hasFin {
		w.g.finish, w.g.hasFin = FinishStop, true
	}
	w.g.released = true
	return w.release()
}
