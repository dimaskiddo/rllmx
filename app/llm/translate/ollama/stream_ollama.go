package ollama

import (
	"fmt"
	"rllmx/app/llm/translate"
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
func (OllamaCodec) NewStreamReader(c *translate.Ctx) translate.StreamReader {
	return &ollamaReader{}
}

func (r *ollamaReader) Flush() []translate.Chunk { return nil }

func (r *ollamaReader) Read(_ string, data []byte) []translate.Chunk {
	if !gjson.ValidBytes(data) {
		return nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil
	}
	if e := g.Get("error"); e.Exists() && !g.Get("message").Exists() {
		return []translate.Chunk{{Err: translate.ReadErrorObject(e)}}
	}
	ch := translate.Chunk{Model: g.Get("model").String()}
	ch.Text = g.Get("message.content").String()
	ch.Reasoning = g.Get("message.thinking").String()
	for _, tc := range g.Get("message.tool_calls").Array() {
		args := tc.Get("function.arguments").Raw
		if args == "" {
			args = "{}"
		}
		idx := r.calls
		ch.ToolCalls = append(ch.ToolCalls, translate.ToolCallDelta{Index: idx, ID: fmt.Sprintf("call_%d", idx), Name: tc.Get("function.name").String(), Arguments: args})
		r.calls++
	}
	if g.Get("done").Bool() && !r.done {
		r.done = true
		reason := translate.FinishStop
		switch g.Get("done_reason").String() {
		case "length", "max_tokens":
			reason = translate.FinishLength
		case "tool_calls", "tool_use":
			reason = translate.FinishToolCalls
		}
		ch.FinishReason = translate.FinishWithCalls(reason, r.calls)
		us := translate.NormalizeUsage(translate.Usage{Prompt: g.Get("prompt_eval_count").Int(), Completion: g.Get("eval_count").Int(), Cached: g.Get("prompt_eval_cached_count").Int()})
		ch.Usage = &us
	}
	if !r.started {
		r.started = true
		ch.Role = translate.RoleAssistant
	}
	if ch.Text == "" && ch.Reasoning == "" && len(ch.ToolCalls) == 0 && ch.FinishReason == "" && ch.Role == "" {
		return nil
	}
	return []translate.Chunk{ch}
}

// ollamaWriter writes /api/chat lines. Exactly one line has done:true.
type ollamaWriter struct {
	g       translate.Gate
	started bool
	model   string
	calls   []translate.ToolCallDelta
}

// NewStreamWriter returns the writer for Ollama streams.
func (OllamaCodec) NewStreamWriter(c *translate.Ctx) translate.StreamWriter {
	return &ollamaWriter{}
}

func (w *ollamaWriter) Held() bool { return w.g.Held() }

func (w *ollamaWriter) line(msg translate.Obj, extra translate.Obj) translate.Frame {
	o := translate.Obj{"model": w.model, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "message": msg}
	for k, v := range extra {
		o[k] = v
	}
	return translate.Frame{Data: translate.FrameData(o)}
}

func (w *ollamaWriter) Write(c translate.Chunk) []translate.Frame {
	if c.Err != nil {
		return nil
	}
	if !w.started {
		w.started = true
		w.model = c.Model
	}
	var out []translate.Frame
	for _, tc := range c.ToolCalls {
		w.hold(tc)
	}
	if c.Text != "" || c.Reasoning != "" {
		msg := translate.Obj{"role": "assistant", "content": c.Text}
		if c.Reasoning != "" {
			msg["thinking"] = c.Reasoning
		}
		out = append(out, w.line(msg, translate.Obj{"done": false}))
	}
	if w.g.See(c) {
		out = append(out, w.release()...)
	}
	return out
}

// hold collects the pieces of a call; an Ollama line carries it whole.
func (w *ollamaWriter) hold(tc translate.ToolCallDelta) {
	for i, have := range w.calls {
		if have.Index == tc.Index {
			w.calls[i].Arguments += tc.Arguments
			return
		}
	}
	w.calls = append(w.calls, tc)
}

func (w *ollamaWriter) release() []translate.Frame {
	var out []translate.Frame
	if len(w.calls) > 0 {
		var calls []any
		for i, tc := range w.calls {
			calls = append(calls, translate.Obj{"function": translate.Obj{"index": i, "name": tc.Name, "arguments": translate.RawOrEmpty(tc.Arguments)}})
		}
		w.calls = nil
		out = append(out, w.line(translate.Obj{"role": "assistant", "content": "", "tool_calls": calls}, translate.Obj{"done": false}))
	}
	reason := "stop"
	if w.g.Finish == translate.FinishLength {
		reason = "length"
	}
	u := w.g.Usage
	return append(out, w.line(translate.Obj{"role": "assistant", "content": ""}, translate.Obj{
		"done": true, "done_reason": reason, "prompt_eval_count": u.Prompt, "eval_count": u.Completion,
	}))
}

func (w *ollamaWriter) Close() []translate.Frame {
	if !w.started || w.g.Released {
		return nil
	}
	if !w.g.HasFinish {
		w.g.Finish, w.g.HasFinish = translate.FinishStop, true
	}
	w.g.Released = true
	return w.release()
}
