package gemini

import (
	"fmt"
	"rllmx/app/llm/translate"
	"strings"

	"github.com/tidwall/gjson"
)

// geminiReader reads generateContent stream chunks. A function call arrives
// whole in one part.
type geminiReader struct {
	started bool
	calls   int
	pending string
	done    bool
}

// NewStreamReader returns the reader for Gemini streams.
func (GeminiCodec) NewStreamReader(c *translate.Ctx) translate.StreamReader {
	return &geminiReader{}
}

func (r *geminiReader) Flush() []translate.Chunk { return nil }

func (r *geminiReader) Read(_ string, data []byte) []translate.Chunk {
	if !gjson.ValidBytes(data) {
		return nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil
	}
	if e := g.Get("error"); e.IsObject() {
		return []translate.Chunk{{Err: &translate.ChunkError{Type: e.Get("status").String(), Message: e.Get("message").String()}}}
	}
	if w := g.Get("response"); w.IsObject() && !g.Get("candidates").Exists() {
		g = w
	}
	ch := translate.Chunk{ID: g.Get("responseId").String(), Model: g.Get("modelVersion").String()}
	if u := g.Get("usageMetadata"); u.IsObject() {
		us := translate.ReadGeminiUsage(u)
		ch.Usage = &us
	}
	cand := g.Get("candidates.0")
	for _, p := range cand.Get("content.parts").Array() {
		if sig := translate.FirstRaw(p.Get("thoughtSignature").String(), p.Get("thought_signature").String()); sig != "" {
			r.pending = sig
		}
		switch {
		case p.Get("functionCall").Exists():
			fc := p.Get("functionCall")
			id := fc.Get("id").String()
			if id == "" {
				id = fmt.Sprintf("%s-%d", fc.Get("name").String(), r.calls)
			}
			args := fc.Get("args").Raw
			if args == "" {
				args = "{}"
			}
			ch.ToolCalls = append(ch.ToolCalls, translate.ToolCallDelta{Index: r.calls, ID: id, Name: fc.Get("name").String(), Arguments: args, Signature: r.pending})
			r.calls++
			r.pending = ""
		case p.Get("text").Exists():
			if p.Get("thought").Bool() {
				ch.Reasoning += p.Get("text").String()
			} else {
				ch.Text += p.Get("text").String()
			}
		case p.Get("inlineData").Exists():
			d := p.Get("inlineData")
			mt := d.Get("mimeType").String()
			if strings.HasPrefix(mt, "image/") {
				ch.Images = append(ch.Images, translate.Part{Type: translate.PartImage, MediaType: mt, Data: d.Get("data").String()})
			}
		}
	}
	if f := cand.Get("finishReason").String(); f != "" && !r.done {
		r.done = true
		ch.FinishReason = translate.FinishWithCalls(translate.FinishFromGemini(f), r.calls)
	}
	if !r.started {
		r.started = true
		ch.Role = translate.RoleAssistant
	}
	if ch.Text == "" && ch.Reasoning == "" && len(ch.ToolCalls) == 0 && len(ch.Images) == 0 && ch.FinishReason == "" && ch.Usage == nil && ch.Role == "" {
		return nil
	}
	return []translate.Chunk{ch}
}

// geminiWriter writes generateContent stream chunks.
type geminiWriter struct {
	g       translate.Gate
	c       *translate.Ctx
	id      string
	model   string
	started bool
	calls   []translate.ToolCallDelta
}

// NewStreamWriter returns the writer for Gemini streams.
func (GeminiCodec) NewStreamWriter(c *translate.Ctx) translate.StreamWriter {
	return &geminiWriter{c: c}
}

func (w *geminiWriter) Held() bool { return w.g.Held() }

func (w *geminiWriter) frame(parts []any, finish string, usage *translate.Usage) translate.Frame {
	cand := translate.Obj{"index": 0}
	if parts != nil {
		cand["content"] = translate.Obj{"role": "model", "parts": parts}
	}
	if finish != "" {
		cand["finishReason"] = finish
	}
	o := translate.Obj{"candidates": []any{cand}, "modelVersion": w.model, "responseId": w.id}
	if usage != nil {
		u := translate.Obj{"promptTokenCount": usage.Prompt, "candidatesTokenCount": usage.Completion - usage.Reasoning, "totalTokenCount": usage.Prompt + usage.Completion}
		if usage.Reasoning > 0 {
			u["thoughtsTokenCount"] = usage.Reasoning
		}
		if usage.Cached > 0 {
			u["cachedContentTokenCount"] = usage.Cached
		}
		o["usageMetadata"] = u
	}
	return translate.Frame{Data: translate.FrameData(o)}
}

func (w *geminiWriter) Write(c translate.Chunk) []translate.Frame {
	if c.Err != nil {
		return nil
	}
	if !w.started {
		w.started = true
		w.id, w.model = c.ID, c.Model
	}
	var parts []any
	if c.Reasoning != "" {
		parts = append(parts, translate.Obj{"text": c.Reasoning, "thought": true})
	}
	if c.Text != "" {
		parts = append(parts, translate.Obj{"text": c.Text})
	}
	for _, im := range c.Images {
		parts = append(parts, translate.Obj{"inlineData": translate.Obj{"mimeType": im.MediaType, "data": im.Data}})
	}
	for _, tc := range c.ToolCalls {
		w.hold(tc)
	}
	var out []translate.Frame
	if len(parts) > 0 {
		out = append(out, w.frame(parts, "", nil))
	}
	if w.g.See(c) {
		out = append(out, w.release()...)
	}
	return out
}

// hold collects the pieces of a call; Gemini wants it whole.
func (w *geminiWriter) hold(tc translate.ToolCallDelta) {
	for i, have := range w.calls {
		if have.Index == tc.Index {
			w.calls[i].Arguments += tc.Arguments
			if tc.Signature != "" {
				w.calls[i].Signature = tc.Signature
			}
			return
		}
	}
	w.calls = append(w.calls, tc)
}

func (w *geminiWriter) release() []translate.Frame {
	var out []translate.Frame
	if len(w.calls) > 0 {
		var parts []any
		for _, tc := range w.calls {
			part := translate.Obj{"functionCall": translate.Obj{"id": tc.ID, "name": tc.Name, "args": translate.RawOrEmpty(tc.Arguments)}}
			call := translate.ToolCall{ID: tc.ID, Signature: tc.Signature}
			if sig := w.c.SignatureFor(call); sig != "" {
				part["thoughtSignature"] = sig
				if w.c.Calls != nil {
					w.c.Calls.Put(tc.ID, sig)
				}
			}
			parts = append(parts, part)
		}
		out = append(out, w.frame(parts, "", nil))
		w.calls = nil
	}
	u := w.g.Usage
	return append(out, w.frame([]any{translate.Obj{"text": ""}}, translate.FinishToGemini(w.g.Finish), &u))
}

func (w *geminiWriter) Close() []translate.Frame {
	if !w.started || w.g.Released {
		return nil
	}
	if !w.g.HasFinish {
		w.g.Finish, w.g.HasFinish = translate.FinishStop, true
	}
	w.g.Released = true
	return w.release()
}
