package translate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// claudeReader reads Messages API events.
type claudeReader struct {
	c       *Ctx
	started bool
	// kind is what each content block index holds: text, thinking, tool, skip.
	kind   map[int]string
	tool   map[int]int
	nTools int
	finish bool
}

// NewStreamReader returns the reader for Messages API streams.
func (ClaudeCodec) NewStreamReader(c *Ctx) StreamReader {
	return &claudeReader{c: c, kind: map[int]string{}, tool: map[int]int{}}
}

func (r *claudeReader) Flush() []Chunk { return nil }

func (r *claudeReader) Read(event string, data []byte) ([]Chunk, error) {
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
	idx := int(g.Get("index").Int())
	switch typ {
	case "message_start":
		r.started = true
		ch := Chunk{ID: g.Get("message.id").String(), Model: g.Get("message.model").String(), Role: RoleAssistant}
		if u := g.Get("message.usage"); u.IsObject() {
			us := readClaudeUsage(u)
			ch.Usage = &us
		}
		return []Chunk{ch}, nil
	case "content_block_start":
		b := g.Get("content_block")
		switch b.Get("type").String() {
		case "text":
			r.kind[idx] = "text"
			if t := b.Get("text").String(); t != "" {
				return []Chunk{{Text: t}}, nil
			}
		case "thinking":
			r.kind[idx] = "thinking"
			if t := b.Get("thinking").String(); t != "" {
				return []Chunk{{Reasoning: t}}, nil
			}
		case "tool_use":
			r.kind[idx] = "tool"
			n := r.nTools
			r.nTools++
			r.tool[idx] = n
			return []Chunk{{ToolCalls: []ToolCallDelta{{Index: n, ID: b.Get("id").String(), Name: r.c.RestoreTool(b.Get("name").String())}}}}, nil
		default:
			r.kind[idx] = "skip"
		}
	case "content_block_delta":
		d := g.Get("delta")
		switch d.Get("type").String() {
		case "text_delta":
			if r.kind[idx] != "skip" && d.Get("text").String() != "" {
				return []Chunk{{Text: d.Get("text").String()}}, nil
			}
		case "thinking_delta":
			if d.Get("thinking").String() != "" {
				return []Chunk{{Reasoning: d.Get("thinking").String()}}, nil
			}
		case "signature_delta":
			if d.Get("signature").String() != "" {
				return []Chunk{{Signature: d.Get("signature").String()}}, nil
			}
		case "input_json_delta":
			n, ok := r.tool[idx]
			if ok && r.kind[idx] == "tool" && d.Get("partial_json").String() != "" {
				return []Chunk{{ToolCalls: []ToolCallDelta{{Index: n, Arguments: d.Get("partial_json").String()}}}}, nil
			}
		}
	case "message_delta":
		ch := Chunk{}
		if u := g.Get("usage"); u.IsObject() {
			us := readClaudeUsage(u)
			ch.Usage = &us
		}
		if s := g.Get("delta.stop_reason").String(); s != "" {
			r.finish = true
			ch.FinishReason = FinishWithCalls(FinishFromClaude(s), r.nTools)
			ch.Text = g.Get("delta.stop_details.explanation").String()
		}
		return []Chunk{ch}, nil
	case "message_stop":
		if !r.finish && r.started {
			r.finish = true
			return []Chunk{{FinishReason: FinishWithCalls(FinishStop, r.nTools)}}, nil
		}
	case "error":
		return []Chunk{{Err: readErrorObject(g.Get("error"))}}, nil
	}
	return nil, nil
}

// claudeWriter writes Messages API events.
type claudeWriter struct {
	g         gate
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
func (ClaudeCodec) NewStreamWriter(c *Ctx) StreamWriter {
	return &claudeWriter{thinking: -1, text: -1, byIndex: map[int]*claudeCall{}}
}

func (w *claudeWriter) Held() bool { return w.g.held() }

func claudeFrame(typ string, o obj) Frame {
	o["type"] = typ
	b, _ := json.Marshal(o)
	return Frame{Event: typ, Data: b}
}

func (w *claudeWriter) begin(c Chunk, out []Frame) []Frame {
	if w.started {
		return out
	}
	w.started = true
	w.id, w.model = c.ID, c.Model
	id := strings.TrimPrefix(w.id, "chatcmpl-")
	if !strings.HasPrefix(id, "msg_") {
		id = "msg_" + id
	}
	msg := obj{"id": id, "type": "message", "role": "assistant", "model": w.model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil, "usage": obj{"input_tokens": 0, "output_tokens": 0}}
	return append(out, claudeFrame("message_start", obj{"message": msg}))
}

func (w *claudeWriter) stop(block *int, out []Frame) []Frame {
	if *block < 0 {
		return out
	}
	out = append(out, claudeFrame("content_block_stop", obj{"index": *block}))
	*block = -1
	return out
}

func (w *claudeWriter) Write(c Chunk) ([]Frame, error) {
	if c.Err != nil {
		return nil, nil
	}
	var out []Frame
	hasOutput := c.Text != "" || c.Reasoning != "" || c.Signature != "" || len(c.ToolCalls) > 0
	if hasOutput {
		out = w.begin(c, out)
	}
	if c.Reasoning != "" || c.Signature != "" {
		out = w.stop(&w.text, out)
		if w.thinking < 0 {
			w.thinking = w.next
			w.next++
			out = append(out, claudeFrame("content_block_start", obj{"index": w.thinking, "content_block": obj{"type": "thinking", "thinking": ""}}))
		}
		if c.Reasoning != "" {
			out = append(out, claudeFrame("content_block_delta", obj{"index": w.thinking, "delta": obj{"type": "thinking_delta", "thinking": c.Reasoning}}))
		}
		if c.Signature != "" {
			out = append(out, claudeFrame("content_block_delta", obj{"index": w.thinking, "delta": obj{"type": "signature_delta", "signature": c.Signature}}))
		}
	}
	if c.Text != "" {
		out = w.stop(&w.thinking, out)
		if w.text < 0 {
			w.text = w.next
			w.next++
			out = append(out, claudeFrame("content_block_start", obj{"index": w.text, "content_block": obj{"type": "text", "text": ""}}))
		}
		out = append(out, claudeFrame("content_block_delta", obj{"index": w.text, "delta": obj{"type": "text_delta", "text": c.Text}}))
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
			out = append(out, claudeFrame("content_block_start", obj{"index": call.block,
				"content_block": obj{"type": "tool_use", "id": call.id, "name": call.name, "input": obj{}}}))
		}
		call.args.WriteString(tc.Arguments)
	}
	if w.g.see(c) {
		out = w.begin(c, out)
		out = append(out, w.release()...)
	}
	return out, nil
}

// release closes every open block and sends the final events.
func (w *claudeWriter) release() []Frame {
	var out []Frame
	out = w.stop(&w.thinking, out)
	out = w.stop(&w.text, out)
	for _, call := range w.tools {
		args := repairToolArgs(call.name, call.args.String())
		out = append(out, claudeFrame("content_block_delta", obj{"index": call.block, "delta": obj{"type": "input_json_delta", "partial_json": args}}))
		out = append(out, claudeFrame("content_block_stop", obj{"index": call.block}))
	}
	w.tools = nil
	finish := FinishWithCalls(w.g.finish, len(w.byIndex))
	u := w.g.usage
	use := obj{"input_tokens": max(u.Prompt-u.Cached-u.CacheWrite, 0), "output_tokens": u.Completion}
	if u.Cached > 0 {
		use["cache_read_input_tokens"] = u.Cached
	}
	if u.CacheWrite > 0 {
		use["cache_creation_input_tokens"] = u.CacheWrite
	}
	out = append(out, claudeFrame("message_delta", obj{"delta": obj{"stop_reason": FinishToClaude(finish), "stop_sequence": nil}, "usage": use}))
	return append(out, claudeFrame("message_stop", obj{}))
}

func (w *claudeWriter) Close() []Frame {
	if !w.started || w.g.released {
		return nil
	}
	if !w.g.hasFin {
		w.g.finish, w.g.hasFin = FinishStop, true
	}
	w.g.released = true
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
