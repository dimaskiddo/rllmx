package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// responsesReader reads Responses API events.
type responsesReader struct {
	started bool
	calls   int
	// byItem maps an output item id to the tool-call index it opened.
	byItem  map[string]int
	hadArgs map[int]bool
	last    int
	done    bool
}

// NewStreamReader returns the reader for Responses streams.
func (ResponsesCodec) NewStreamReader(c *Ctx) StreamReader {
	return &responsesReader{byItem: map[string]int{}, hadArgs: map[int]bool{}, last: -1}
}

func (r *responsesReader) role(ch *Chunk) {
	if !r.started {
		r.started = true
		ch.Role = RoleAssistant
	}
}

func (r *responsesReader) Flush() []Chunk {
	if !r.started || r.done {
		return nil
	}
	r.done = true
	return []Chunk{{FinishReason: FinishWithCalls(FinishStop, r.calls)}}
}

func (r *responsesReader) Read(event string, data []byte) ([]Chunk, error) {
	if !gjson.ValidBytes(data) {
		return nil, nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil, nil
	}
	typ := g.Get("type").String()
	if typ == "" {
		typ = event
	}
	switch typ {
	case "response.created":
		ch := Chunk{ID: g.Get("response.id").String(), Model: g.Get("response.model").String(), Created: g.Get("response.created_at").Int()}
		r.role(&ch)
		return []Chunk{ch}, nil
	case "response.output_text.delta":
		if d := g.Get("delta").String(); d != "" {
			ch := Chunk{Text: d}
			r.role(&ch)
			return []Chunk{ch}, nil
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if d := g.Get("delta").String(); d != "" {
			ch := Chunk{Reasoning: d}
			r.role(&ch)
			return []Chunk{ch}, nil
		}
	case "response.output_item.added":
		it := g.Get("item")
		kind := it.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			return nil, nil
		}
		id := it.Get("id").String()
		idx, seen := r.byItem[id]
		if !seen || id == "" {
			idx = r.calls
			r.calls++
			if id != "" {
				r.byItem[id] = idx
			}
		}
		r.last = idx
		callID := firstRaw(it.Get("call_id").String(), id, fmt.Sprintf("call_stream_%d", idx))
		ch := Chunk{ToolCalls: []ToolCallDelta{{Index: idx, ID: callID, Name: it.Get("name").String(), Custom: kind == "custom_tool_call"}}}
		r.role(&ch)
		return []Chunk{ch}, nil
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		idx, ok := r.byItem[g.Get("item_id").String()]
		if !ok {
			idx = r.last
		}
		if idx < 0 || g.Get("delta").String() == "" {
			return nil, nil
		}
		r.hadArgs[idx] = true
		return []Chunk{{ToolCalls: []ToolCallDelta{{Index: idx, Arguments: g.Get("delta").String()}}}}, nil
	case "response.output_item.done":
		it := g.Get("item")
		kind := it.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			return nil, nil
		}
		idx, ok := r.byItem[it.Get("id").String()]
		if !ok || r.hadArgs[idx] {
			return nil, nil
		}
		args := it.Get("arguments").String()
		if kind == "custom_tool_call" {
			args = mustJSON(map[string]string{"input": it.Get("input").String()})
		}
		if args == "" {
			return nil, nil
		}
		r.hadArgs[idx] = true
		return []Chunk{{ToolCalls: []ToolCallDelta{{Index: idx, Arguments: args}}}}, nil
	case "response.completed", "response.done":
		if r.done {
			return nil, nil
		}
		r.done = true
		ch := Chunk{FinishReason: FinishWithCalls(FinishStop, r.calls)}
		switch g.Get("response.incomplete_details.reason").String() {
		case "max_output_tokens":
			ch.FinishReason = FinishLength
		case "content_filter":
			ch.FinishReason = FinishFilter
		}
		if u := g.Get("response.usage"); u.IsObject() {
			us := readResponsesUsage(u)
			ch.Usage = &us
		}
		r.role(&ch)
		return []Chunk{ch}, nil
	case "error", "response.failed":
		e := g.Get("error")
		if !e.Exists() {
			e = g.Get("response.error")
		}
		if !e.Exists() {
			e = g
		}
		r.done = true
		return []Chunk{{Err: &ChunkError{Type: e.Get("type").String(), Message: e.Get("message").String()}}}, nil
	}
	return nil, nil
}

// responsesWriter writes Responses API events.
type responsesWriter struct {
	g        gate
	custom   map[string]bool
	seq      int
	started  bool
	id       string
	model    string
	created  int64
	next     int
	reason   *rItem
	message  *rItem
	tools    map[int]*rItem
	order    []*rItem
	finished map[int]obj
	think    thinkSplitter
	sent     bool
}

type rItem struct {
	id, callID, name string
	index            int
	buf              strings.Builder
	custom, closed   bool
}

// NewStreamWriter returns the writer for Responses streams.
func (ResponsesCodec) NewStreamWriter(c *Ctx) StreamWriter {
	return &responsesWriter{custom: c.CustomTools, tools: map[int]*rItem{}, finished: map[int]obj{}}
}

func (w *responsesWriter) Held() bool { return w.g.held() }

func (w *responsesWriter) ev(name string, o obj) Frame {
	w.seq++
	o["type"] = name
	o["sequence_number"] = w.seq
	b, _ := json.Marshal(o)
	return Frame{Event: name, Data: b}
}

func (w *responsesWriter) begin(c Chunk, out []Frame) []Frame {
	if w.started {
		return out
	}
	w.started = true
	w.id, w.model = c.ID, c.Model
	if w.id == "" {
		w.id = "resp_stream"
	}
	w.created = c.Created
	if w.created == 0 {
		w.created = time.Now().Unix()
	}
	resp := obj{"id": w.id, "object": "response", "created_at": w.created, "model": w.model, "status": "in_progress", "output": []any{}}
	return append(out, w.ev("response.created", obj{"response": resp}), w.ev("response.in_progress", obj{"response": resp}))
}

func (w *responsesWriter) closeReasoning(out []Frame) []Frame {
	it := w.reason
	if it == nil || it.closed {
		return out
	}
	it.closed = true
	text := it.buf.String()
	part := obj{"type": "summary_text", "text": text}
	out = append(out,
		w.ev("response.reasoning_summary_text.done", obj{"item_id": it.id, "output_index": it.index, "summary_index": 0, "text": text}),
		w.ev("response.reasoning_summary_part.done", obj{"item_id": it.id, "output_index": it.index, "summary_index": 0, "part": part}))
	item := obj{"id": it.id, "type": "reasoning", "summary": []any{part}}
	w.finished[it.index] = item
	return append(out, w.ev("response.output_item.done", obj{"output_index": it.index, "item": item}))
}

func (w *responsesWriter) closeMessage(out []Frame) []Frame {
	it := w.message
	if it == nil || it.closed {
		return out
	}
	it.closed = true
	text := it.buf.String()
	part := obj{"type": "output_text", "text": text, "annotations": []any{}}
	out = append(out,
		w.ev("response.output_text.done", obj{"item_id": it.id, "output_index": it.index, "content_index": 0, "text": text}),
		w.ev("response.content_part.done", obj{"item_id": it.id, "output_index": it.index, "content_index": 0, "part": part}))
	item := obj{"id": it.id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
	w.finished[it.index] = item
	return append(out, w.ev("response.output_item.done", obj{"output_index": it.index, "item": item}))
}

func (w *responsesWriter) reasoningDelta(s string, out []Frame) []Frame {
	if s == "" {
		return out
	}
	out = w.closeMessage(out)
	if w.reason == nil {
		it := &rItem{id: fmt.Sprintf("rs_%s_%d", w.id, w.next), index: w.next}
		w.next++
		w.reason = it
		out = append(out,
			w.ev("response.output_item.added", obj{"output_index": it.index, "item": obj{"id": it.id, "type": "reasoning", "summary": []any{}}}),
			w.ev("response.reasoning_summary_part.added", obj{"item_id": it.id, "output_index": it.index, "summary_index": 0, "part": obj{"type": "summary_text", "text": ""}}))
	}
	w.reason.buf.WriteString(s)
	return append(out, w.ev("response.reasoning_summary_text.delta", obj{"item_id": w.reason.id, "output_index": w.reason.index, "summary_index": 0, "delta": s}))
}

func (w *responsesWriter) textDelta(s string, out []Frame) []Frame {
	if s == "" {
		return out
	}
	out = w.closeReasoning(out)
	if w.message == nil {
		it := &rItem{id: fmt.Sprintf("msg_%s_%d", w.id, w.next), index: w.next}
		w.next++
		w.message = it
		out = append(out,
			w.ev("response.output_item.added", obj{"output_index": it.index, "item": obj{"id": it.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}),
			w.ev("response.content_part.added", obj{"item_id": it.id, "output_index": it.index, "content_index": 0, "part": obj{"type": "output_text", "text": "", "annotations": []any{}}}))
	}
	w.message.buf.WriteString(s)
	return append(out, w.ev("response.output_text.delta", obj{"item_id": w.message.id, "output_index": w.message.index, "content_index": 0, "delta": s}))
}

func (w *responsesWriter) content(s string, out []Frame) []Frame {
	for _, seg := range w.think.split(s) {
		if seg.reasoning {
			out = w.reasoningDelta(seg.text, out)
		} else {
			out = w.textDelta(seg.text, out)
		}
	}
	return out
}

func (w *responsesWriter) toolDelta(tc ToolCallDelta, out []Frame) []Frame {
	it, open := w.tools[tc.Index]
	if !open {
		if tc.Name == "" && tc.ID == "" {
			return out
		}
		out = w.closeReasoning(out)
		out = w.closeMessage(out)
		it = &rItem{index: w.next, callID: tc.ID, name: tc.Name, custom: tc.Custom || w.custom[tc.Name]}
		w.next++
		prefix := "fc_"
		kind := "function_call"
		if it.custom {
			prefix, kind = "ctc_", "custom_tool_call"
		}
		it.id = prefix + it.callID
		w.tools[tc.Index] = it
		w.order = append(w.order, it)
		added := obj{"id": it.id, "type": kind, "call_id": it.callID, "name": it.name, "status": "in_progress"}
		if it.custom {
			added["input"] = ""
		} else {
			added["arguments"] = ""
		}
		out = append(out, w.ev("response.output_item.added", obj{"output_index": it.index, "item": added}))
	}
	if tc.Arguments == "" {
		return out
	}
	it.buf.WriteString(tc.Arguments)
	if !it.custom {
		out = append(out, w.ev("response.function_call_arguments.delta", obj{"item_id": it.id, "output_index": it.index, "delta": tc.Arguments}))
	}
	return out
}

func (w *responsesWriter) closeTools(out []Frame) []Frame {
	for _, it := range w.order {
		if it.closed {
			continue
		}
		it.closed = true
		args := it.buf.String()
		if args == "" {
			args = "{}"
		}
		var item obj
		if it.custom {
			input := customInput(args)
			out = append(out,
				w.ev("response.custom_tool_call_input.delta", obj{"item_id": it.id, "output_index": it.index, "delta": input}),
				w.ev("response.custom_tool_call_input.done", obj{"item_id": it.id, "output_index": it.index, "input": input}))
			item = obj{"id": it.id, "type": "custom_tool_call", "call_id": it.callID, "name": it.name, "input": input, "status": "completed"}
		} else {
			out = append(out, w.ev("response.function_call_arguments.done", obj{"item_id": it.id, "output_index": it.index, "arguments": args}))
			item = obj{"id": it.id, "type": "function_call", "call_id": it.callID, "name": it.name, "arguments": args, "status": "completed"}
		}
		w.finished[it.index] = item
		out = append(out, w.ev("response.output_item.done", obj{"output_index": it.index, "item": item}))
	}
	return out
}

func (w *responsesWriter) Write(c Chunk) ([]Frame, error) {
	if c.Err != nil {
		return nil, nil
	}
	var out []Frame
	if c.Text != "" || c.Reasoning != "" || len(c.ToolCalls) > 0 {
		out = w.begin(c, out)
	}
	out = w.reasoningDelta(c.Reasoning, out)
	out = w.content(c.Text, out)
	for _, tc := range c.ToolCalls {
		out = w.toolDelta(tc, out)
	}
	if w.g.see(c) {
		out = w.begin(c, out)
		out = append(out, w.release()...)
	}
	return out, nil
}

func (w *responsesWriter) release() []Frame {
	var out []Frame
	for _, seg := range w.think.drain() {
		if seg.reasoning {
			out = w.reasoningDelta(seg.text, out)
		} else {
			out = w.textDelta(seg.text, out)
		}
	}
	out = w.closeMessage(out)
	out = w.closeReasoning(out)
	out = w.closeTools(out)
	w.sent = true
	u := w.g.usage
	use := obj{"input_tokens": u.Prompt, "output_tokens": u.Completion, "total_tokens": u.Prompt + u.Completion}
	if u.Cached > 0 {
		use["input_tokens_details"] = obj{"cached_tokens": u.Cached}
	}
	if u.Reasoning > 0 {
		use["output_tokens_details"] = obj{"reasoning_tokens": u.Reasoning}
	}
	resp := obj{"id": w.id, "object": "response", "created_at": w.created, "model": w.model, "status": "completed", "output": w.sorted(), "usage": use}
	name := "response.completed"
	switch w.g.finish {
	case FinishLength:
		resp["status"] = "incomplete"
		resp["incomplete_details"] = obj{"reason": "max_output_tokens"}
	case FinishFilter:
		resp["status"] = "incomplete"
		resp["incomplete_details"] = obj{"reason": "content_filter"}
	}
	return append(out, w.ev(name, obj{"response": resp}))
}

// sorted returns the finished items by output index.
func (w *responsesWriter) sorted() []any {
	out := make([]any, 0, len(w.finished))
	for i := 0; i < w.next; i++ {
		if it, ok := w.finished[i]; ok {
			out = append(out, it)
		}
	}
	return out
}

func (w *responsesWriter) Close() []Frame {
	if w.g.released {
		return nil
	}
	if !w.g.hasFin {
		if !w.started {
			return nil
		}
		w.g.finish, w.g.hasFin = FinishStop, true
	}
	w.g.released = true
	return w.release()
}
