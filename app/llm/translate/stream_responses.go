package translate

import (
	"fmt"
	"strconv"
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

func (r *responsesReader) Read(event string, data []byte) []Chunk {
	if !gjson.ValidBytes(data) {
		return nil
	}
	g := gjson.ParseBytes(data)
	if !g.IsObject() {
		return nil
	}
	typ := g.Get("type").String()
	if typ == "" {
		typ = event
	}
	switch typ {
	case "response.created":
		ch := Chunk{ID: g.Get("response.id").String(), Model: g.Get("response.model").String(), Created: g.Get("response.created_at").Int()}
		r.role(&ch)
		return []Chunk{ch}
	case "response.output_text.delta":
		if d := g.Get("delta").String(); d != "" {
			ch := Chunk{Text: d}
			r.role(&ch)
			return []Chunk{ch}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if d := g.Get("delta").String(); d != "" {
			ch := Chunk{Reasoning: d}
			r.role(&ch)
			return []Chunk{ch}
		}
	case "response.output_item.added":
		it := g.Get("item")
		kind := it.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			return nil
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
		return []Chunk{ch}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		idx, ok := r.byItem[g.Get("item_id").String()]
		if !ok {
			idx = r.last
		}
		if idx < 0 || g.Get("delta").String() == "" {
			return nil
		}
		r.hadArgs[idx] = true
		return []Chunk{{ToolCalls: []ToolCallDelta{{Index: idx, Arguments: g.Get("delta").String()}}}}
	case "response.output_item.done":
		it := g.Get("item")
		kind := it.Get("type").String()
		if kind != "function_call" && kind != "custom_tool_call" {
			return nil
		}
		idx, ok := r.byItem[it.Get("id").String()]
		if !ok || r.hadArgs[idx] {
			return nil
		}
		args := it.Get("arguments").String()
		if kind == "custom_tool_call" {
			args = mustJSON(map[string]string{"input": it.Get("input").String()})
		}
		if args == "" {
			return nil
		}
		r.hadArgs[idx] = true
		return []Chunk{{ToolCalls: []ToolCallDelta{{Index: idx, Arguments: args}}}}
	case "response.completed", "response.done":
		if r.done {
			return nil
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
		return []Chunk{ch}
	case "error", "response.failed":
		e := g.Get("error")
		if !e.Exists() {
			e = g.Get("response.error")
		}
		if !e.Exists() {
			e = g
		}
		r.done = true
		return []Chunk{{Err: &ChunkError{Type: e.Get("type").String(), Message: e.Get("message").String()}}}
	}
	return nil
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
	finished map[int]Obj
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
	return &responsesWriter{custom: c.CustomTools, tools: map[int]*rItem{}, finished: map[int]Obj{}}
}

func (w *responsesWriter) Held() bool { return w.g.held() }

func (w *responsesWriter) ev(name string, o Obj) Frame {
	w.seq++
	o["type"] = name
	o["sequence_number"] = w.seq
	return Frame{Event: name, Data: frameData(o)}
}

// textDeltaEvent is ev for the delta events that carry one text fragment
// (output_text and reasoning_summary_text), in the sorted key order ev gives.
// partKey is "content_index" or "summary_index", whichever the event names; the
// first sorts before "delta" and the second after "sequence_number".
func (w *responsesWriter) textDeltaEvent(name, partKey, itemID string, outputIndex int, delta string) Frame {
	w.seq++
	b := make([]byte, 0, 160+len(delta)+len(itemID))
	b = append(b, '{')
	if partKey == "content_index" {
		b = append(b, `"content_index":0,`...)
	}
	b = append(b, `"delta":`...)
	b = appendJSONString(b, delta)
	b = append(b, `,"item_id":`...)
	b = appendJSONString(b, itemID)
	b = append(b, `,"output_index":`...)
	b = strconv.AppendInt(b, int64(outputIndex), 10)
	b = append(b, `,"sequence_number":`...)
	b = strconv.AppendInt(b, int64(w.seq), 10)
	if partKey == "summary_index" {
		b = append(b, `,"summary_index":0`...)
	}
	b = append(b, `,"type":"`...)
	b = append(b, name...)
	b = append(b, `"}`...)
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
	resp := Obj{"id": w.id, "object": "response", "created_at": w.created, "model": w.model, "status": "in_progress", "output": []any{}}
	return append(out, w.ev("response.created", Obj{"response": resp}), w.ev("response.in_progress", Obj{"response": resp}))
}

func (w *responsesWriter) closeReasoning(out []Frame) []Frame {
	it := w.reason
	if it == nil || it.closed {
		return out
	}
	it.closed = true
	text := it.buf.String()
	part := Obj{"type": "summary_text", "text": text}
	out = append(out,
		w.ev("response.reasoning_summary_text.done", Obj{"item_id": it.id, "output_index": it.index, "summary_index": 0, "text": text}),
		w.ev("response.reasoning_summary_part.done", Obj{"item_id": it.id, "output_index": it.index, "summary_index": 0, "part": part}))
	item := Obj{"id": it.id, "type": "reasoning", "summary": []any{part}}
	w.finished[it.index] = item
	return append(out, w.ev("response.output_item.done", Obj{"output_index": it.index, "item": item}))
}

func (w *responsesWriter) closeMessage(out []Frame) []Frame {
	it := w.message
	if it == nil || it.closed {
		return out
	}
	it.closed = true
	text := it.buf.String()
	part := Obj{"type": "output_text", "text": text, "annotations": []any{}}
	out = append(out,
		w.ev("response.output_text.done", Obj{"item_id": it.id, "output_index": it.index, "content_index": 0, "text": text}),
		w.ev("response.content_part.done", Obj{"item_id": it.id, "output_index": it.index, "content_index": 0, "part": part}))
	item := Obj{"id": it.id, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
	w.finished[it.index] = item
	return append(out, w.ev("response.output_item.done", Obj{"output_index": it.index, "item": item}))
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
			w.ev("response.output_item.added", Obj{"output_index": it.index, "item": Obj{"id": it.id, "type": "reasoning", "summary": []any{}}}),
			w.ev("response.reasoning_summary_part.added", Obj{"item_id": it.id, "output_index": it.index, "summary_index": 0, "part": Obj{"type": "summary_text", "text": ""}}))
	}
	w.reason.buf.WriteString(s)
	return append(out, w.textDeltaEvent("response.reasoning_summary_text.delta", "summary_index", w.reason.id, w.reason.index, s))
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
			w.ev("response.output_item.added", Obj{"output_index": it.index, "item": Obj{"id": it.id, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}}),
			w.ev("response.content_part.added", Obj{"item_id": it.id, "output_index": it.index, "content_index": 0, "part": Obj{"type": "output_text", "text": "", "annotations": []any{}}}))
	}
	w.message.buf.WriteString(s)
	return append(out, w.textDeltaEvent("response.output_text.delta", "content_index", w.message.id, w.message.index, s))
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
		added := Obj{"id": it.id, "type": kind, "call_id": it.callID, "name": it.name, "status": "in_progress"}
		if it.custom {
			added["input"] = ""
		} else {
			added["arguments"] = ""
		}
		out = append(out, w.ev("response.output_item.added", Obj{"output_index": it.index, "item": added}))
	}
	if tc.Arguments == "" {
		return out
	}
	it.buf.WriteString(tc.Arguments)
	if !it.custom {
		out = append(out, w.ev("response.function_call_arguments.delta", Obj{"item_id": it.id, "output_index": it.index, "delta": tc.Arguments}))
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
		var item Obj
		if it.custom {
			input := customInput(args)
			out = append(out,
				w.ev("response.custom_tool_call_input.delta", Obj{"item_id": it.id, "output_index": it.index, "delta": input}),
				w.ev("response.custom_tool_call_input.done", Obj{"item_id": it.id, "output_index": it.index, "input": input}))
			item = Obj{"id": it.id, "type": "custom_tool_call", "call_id": it.callID, "name": it.name, "input": input, "status": "completed"}
		} else {
			out = append(out, w.ev("response.function_call_arguments.done", Obj{"item_id": it.id, "output_index": it.index, "arguments": args}))
			item = Obj{"id": it.id, "type": "function_call", "call_id": it.callID, "name": it.name, "arguments": args, "status": "completed"}
		}
		w.finished[it.index] = item
		out = append(out, w.ev("response.output_item.done", Obj{"output_index": it.index, "item": item}))
	}
	return out
}

func (w *responsesWriter) Write(c Chunk) []Frame {
	if c.Err != nil {
		return nil
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
	return out
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
	use := Obj{"input_tokens": u.Prompt, "output_tokens": u.Completion, "total_tokens": u.Prompt + u.Completion}
	if u.Cached > 0 {
		use["input_tokens_details"] = Obj{"cached_tokens": u.Cached}
	}
	if u.Reasoning > 0 {
		use["output_tokens_details"] = Obj{"reasoning_tokens": u.Reasoning}
	}
	resp := Obj{"id": w.id, "object": "response", "created_at": w.created, "model": w.model, "status": "completed", "output": w.sorted(), "usage": use}
	name := "response.completed"
	switch w.g.finish {
	case FinishLength:
		resp["status"] = "incomplete"
		resp["incomplete_details"] = Obj{"reason": "max_output_tokens"}
	case FinishFilter:
		resp["status"] = "incomplete"
		resp["incomplete_details"] = Obj{"reason": "content_filter"}
	}
	return append(out, w.ev(name, Obj{"response": resp}))
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
