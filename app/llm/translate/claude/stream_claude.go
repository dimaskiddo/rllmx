package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"rllmx/app/llm/translate"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// claudeReader reads Messages API events.
type claudeReader struct {
	c       *translate.Ctx
	started bool
	// kind is what each content block index holds: text, thinking, tool, skip.
	kind   map[int]string
	tool   map[int]int
	nTools int
	finish bool
}

// NewStreamReader returns the reader for Messages API streams.
func (ClaudeCodec) NewStreamReader(c *translate.Ctx) translate.StreamReader {
	return &claudeReader{c: c, kind: map[int]string{}, tool: map[int]int{}}
}

func (r *claudeReader) Flush() []translate.Chunk { return nil }

func (r *claudeReader) Read(event string, data []byte) []translate.Chunk {
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
	idx := int(g.Get("index").Int())
	switch typ {
	case "message_start":
		r.started = true
		ch := translate.Chunk{ID: g.Get("message.id").String(), Model: g.Get("message.model").String(), Role: translate.RoleAssistant}
		if u := g.Get("message.usage"); u.IsObject() {
			us := translate.ReadClaudeUsage(u)
			ch.Usage = &us
		}
		return []translate.Chunk{ch}
	case "content_block_start":
		b := g.Get("content_block")
		switch b.Get("type").String() {
		case "text":
			r.kind[idx] = "text"
			if t := b.Get("text").String(); t != "" {
				return []translate.Chunk{{Text: t}}
			}
		case "thinking":
			r.kind[idx] = "thinking"
			if t := b.Get("thinking").String(); t != "" {
				return []translate.Chunk{{Reasoning: t}}
			}
		case "tool_use":
			r.kind[idx] = "tool"
			n := r.nTools
			r.nTools++
			r.tool[idx] = n
			return []translate.Chunk{{ToolCalls: []translate.ToolCallDelta{{Index: n, ID: b.Get("id").String(), Name: r.c.RestoreTool(b.Get("name").String())}}}}
		default:
			r.kind[idx] = "skip"
		}
	case "content_block_delta":
		d := g.Get("delta")
		switch d.Get("type").String() {
		case "text_delta":
			if r.kind[idx] != "skip" && d.Get("text").String() != "" {
				return []translate.Chunk{{Text: d.Get("text").String()}}
			}
		case "thinking_delta":
			if d.Get("thinking").String() != "" {
				return []translate.Chunk{{Reasoning: d.Get("thinking").String()}}
			}
		case "signature_delta":
			if d.Get("signature").String() != "" {
				return []translate.Chunk{{Signature: d.Get("signature").String()}}
			}
		case "input_json_delta":
			n, ok := r.tool[idx]
			if ok && r.kind[idx] == "tool" && d.Get("partial_json").String() != "" {
				return []translate.Chunk{{ToolCalls: []translate.ToolCallDelta{{Index: n, Arguments: d.Get("partial_json").String()}}}}
			}
		}
	case "message_delta":
		ch := translate.Chunk{}
		if u := g.Get("usage"); u.IsObject() {
			us := translate.ReadClaudeUsage(u)
			ch.Usage = &us
		}
		if s := g.Get("delta.stop_reason").String(); s != "" {
			r.finish = true
			ch.FinishReason = translate.FinishWithCalls(translate.FinishFromClaude(s), r.nTools)
			ch.Text = g.Get("delta.stop_details.explanation").String()
		}
		return []translate.Chunk{ch}
	case "message_stop":
		if !r.finish && r.started {
			r.finish = true
			return []translate.Chunk{{FinishReason: translate.FinishWithCalls(translate.FinishStop, r.nTools)}}
		}
	case "error":
		return []translate.Chunk{{Err: translate.ReadErrorObject(g.Get("error"))}}
	}
	return nil
}

// claudeWriter writes Messages API events.
type claudeWriter struct {
	g         translate.Gate
	started   bool
	id, model string
	next      int
	thinking  int
	text      int
	tools     []*claudeCall
	byIndex   map[int]*claudeCall
}

type claudeCall struct {
	id, name string
	block    int
	args     strings.Builder
}

// NewStreamWriter returns the writer for Messages API streams.
func (ClaudeCodec) NewStreamWriter(c *translate.Ctx) translate.StreamWriter {
	return &claudeWriter{thinking: -1, text: -1, byIndex: map[int]*claudeCall{}}
}

func (w *claudeWriter) Held() bool { return w.g.Held() }

func claudeFrame(typ string, o translate.Obj) translate.Frame {
	o["type"] = typ
	return translate.Frame{Event: typ, Data: translate.FrameData(o)}
}

// claudeDeltaFrame is the content_block_delta that carries one text or thinking
// fragment, written in the sorted key order claudeFrame would give it.
// kind is the delta type ("text_delta") and field the key holding the text.
func claudeDeltaFrame(index int, kind, field, text string) translate.Frame {
	b := make([]byte, 0, 96+len(text))
	b = append(b, `{"delta":{"`...)
	b = append(b, field...)
	b = append(b, `":`...)
	b = translate.AppendJSONString(b, text)
	b = append(b, `,"type":"`...)
	b = append(b, kind...)
	b = append(b, `"},"index":`...)
	b = strconv.AppendInt(b, int64(index), 10)
	b = append(b, `,"type":"content_block_delta"}`...)
	return translate.Frame{Event: "content_block_delta", Data: b}
}

func (w *claudeWriter) begin(c translate.Chunk, out []translate.Frame) []translate.Frame {
	if w.started {
		return out
	}
	w.started = true
	w.id, w.model = c.ID, c.Model
	id := strings.TrimPrefix(w.id, "chatcmpl-")
	if !strings.HasPrefix(id, "msg_") {
		id = "msg_" + id
	}
	msg := translate.Obj{"id": id, "type": "message", "role": "assistant", "model": w.model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": translate.Obj{"input_tokens": 0, "output_tokens": 0}}
	return append(out, claudeFrame("message_start", translate.Obj{"message": msg}))
}

func (w *claudeWriter) stop(block *int, out []translate.Frame) []translate.Frame {
	if *block < 0 {
		return out
	}
	out = append(out, claudeFrame("content_block_stop", translate.Obj{"index": *block}))
	*block = -1
	return out
}

func (w *claudeWriter) Write(c translate.Chunk) []translate.Frame {
	if c.Err != nil {
		return nil
	}
	var out []translate.Frame
	hasOutput := c.Text != "" || c.Reasoning != "" || c.Signature != "" || len(c.ToolCalls) > 0
	if hasOutput {
		out = w.begin(c, out)
	}
	if c.Reasoning != "" || c.Signature != "" {
		out = w.stop(&w.text, out)
		if w.thinking < 0 {
			w.thinking = w.next
			w.next++
			out = append(out, claudeFrame("content_block_start", translate.Obj{"index": w.thinking, "content_block": translate.Obj{"type": "thinking", "thinking": ""}}))
		}
		if c.Reasoning != "" {
			out = append(out, claudeDeltaFrame(w.thinking, "thinking_delta", "thinking", c.Reasoning))
		}
		if c.Signature != "" {
			out = append(out, claudeFrame("content_block_delta", translate.Obj{"index": w.thinking, "delta": translate.Obj{"type": "signature_delta", "signature": c.Signature}}))
		}
	}
	if c.Text != "" {
		out = w.stop(&w.thinking, out)
		if w.text < 0 {
			w.text = w.next
			w.next++
			out = append(out, claudeFrame("content_block_start", translate.Obj{"index": w.text, "content_block": translate.Obj{"type": "text", "text": ""}}))
		}
		out = append(out, claudeDeltaFrame(w.text, "text_delta", "text", c.Text))
	}
	for _, tc := range c.ToolCalls {
		call, open := w.byIndex[tc.Index]
		if !open {
			out = w.stop(&w.thinking, out)
			out = w.stop(&w.text, out)
			id := tc.ID
			if id == "" {
				id = fmt.Sprintf("toolu_stream_%d", tc.Index)
			}
			call = &claudeCall{id: id, name: tc.Name, block: w.next}
			w.next++
			w.byIndex[tc.Index] = call
			w.tools = append(w.tools, call)
			out = append(out, claudeFrame("content_block_start", translate.Obj{"index": call.block,
				"content_block": translate.Obj{"type": "tool_use", "id": call.id, "name": call.name, "input": translate.Obj{}}}))
		}
		call.args.WriteString(tc.Arguments)
	}
	if w.g.See(c) {
		out = w.begin(c, out)
		out = append(out, w.release()...)
	}
	return out
}

// release closes every open block and sends the final events.
func (w *claudeWriter) release() []translate.Frame {
	var out []translate.Frame
	out = w.stop(&w.thinking, out)
	out = w.stop(&w.text, out)
	for _, call := range w.tools {
		args := repairToolArgs(call.name, call.args.String())
		out = append(out, claudeFrame("content_block_delta", translate.Obj{"index": call.block, "delta": translate.Obj{"type": "input_json_delta", "partial_json": args}}))
		out = append(out, claudeFrame("content_block_stop", translate.Obj{"index": call.block}))
	}
	w.tools = nil
	finish := translate.FinishWithCalls(w.g.Finish, len(w.byIndex))
	u := w.g.Usage
	use := translate.Obj{"input_tokens": max(u.Prompt-u.Cached-u.CacheWrite, 0), "output_tokens": u.Completion}
	if u.Cached > 0 {
		use["cache_read_input_tokens"] = u.Cached
	}
	if u.CacheWrite > 0 {
		use["cache_creation_input_tokens"] = u.CacheWrite
	}
	out = append(out, claudeFrame("message_delta", translate.Obj{"delta": translate.Obj{"stop_reason": translate.FinishToClaude(finish), "stop_sequence": nil}, "usage": use}))
	return append(out, claudeFrame("message_stop", translate.Obj{}))
}

func (w *claudeWriter) Close() []translate.Frame {
	if !w.started || w.g.Released {
		return nil
	}
	if !w.g.HasFinish {
		w.g.Finish, w.g.HasFinish = translate.FinishStop, true
	}
	w.g.Released = true
	return w.release()
}

var (
	digits    = regexp.MustCompile(`^\d+$`)
	signedNum = regexp.MustCompile(`^-?\d+$`)
	pdfPages  = regexp.MustCompile(`^\d+(?:-\d+)?$`)
)

// repairToolArgs fixes the arguments other models send to a client's Read
// tool: numbers sent as strings, an oversized or negative range, and a pages
// value that is not a PDF page range. Anything else, and anything that is not
// a JSON object, passes through untouched.
func repairToolArgs(name, args string) string {
	if strings.TrimSpace(args) == "" {
		return "{}"
	}
	if name != "Read" {
		return args
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(args)))
	dec.UseNumber()
	var m map[string]any
	if dec.Decode(&m) != nil || m == nil {
		return args
	}
	if s, ok := m["limit"].(string); ok && digits.MatchString(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			m["limit"] = n
		}
	}
	if s, ok := m["offset"].(string); ok && signedNum.MatchString(s) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			m["offset"] = n
		}
	}
	if n, ok := asInt(m["limit"]); ok {
		if n > 2000 {
			m["limit"] = 2000
		}
		if n < 1 {
			delete(m, "limit")
		}
	}
	if n, ok := asInt(m["offset"]); ok && n < 0 {
		m["offset"] = 0
	}
	if p, has := m["pages"]; has {
		path, _ := m["file_path"].(string)
		pages, _ := p.(string)
		if !strings.HasSuffix(strings.ToLower(path), ".pdf") || !pdfPages.MatchString(pages) {
			delete(m, "pages")
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return args
	}
	return string(b)
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
