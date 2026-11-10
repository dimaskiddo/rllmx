package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// commandCodeRead reads CommandCode's AI SDK events. Text and reasoning arrive
// as deltas; a tool call arrives either as fragments under one id or whole in a
// single tool-call event, never both.
type commandCodeRead struct {
	c      *Ctx
	opened bool
	// index maps a call's upstream id to the index a client sees, so its
	// fragments land on one call.
	index map[string]int
	calls int
	// streamed marks the ids that arrived as fragments; a whole tool-call event
	// for one of those is the same call repeated and is dropped.
	streamed map[string]bool
	finish   string
	usage    *Usage
}

// NewStreamReader returns the reader for CommandCode streams.
func (CommandCodeCodec) NewStreamReader(c *Ctx) StreamReader {
	return &commandCodeRead{c: c, index: map[string]int{}, streamed: map[string]bool{}}
}

// Flush ends the turn. The upstream sends its own finish event; one that ends
// without it still gets a close, so a client is never left waiting.
func (r *commandCodeRead) Flush() []Chunk {
	if r.finish != "" {
		return nil
	}
	return []Chunk{{FinishReason: FinishWithCalls(FinishStop, r.calls)}}
}

// Read handles one event, given as one JSON object. A `data:` prefix is
// tolerated, as is `[DONE]`.
func (r *commandCodeRead) Read(_ string, data []byte) []Chunk {
	line := strings.TrimSpace(string(data))
	if line == "" || line == "[DONE]" {
		return nil
	}
	if !gjson.Valid(line) {
		return nil
	}
	g := gjson.Parse(line)
	if !g.IsObject() {
		return nil
	}
	typ := g.Get("type").String()
	switch typ {
	case "text-delta":
		return r.emit(Chunk{Text: firstString(g, "text", "delta")})
	case "reasoning-delta":
		return r.emit(Chunk{Reasoning: firstString(g, "text", "delta")})
	case "tool-input-start":
		return r.startCall(g)
	case "tool-input-delta":
		return r.deltaCall(g)
	case "tool-call":
		return r.wholeCall(g)
	case "finish-step":
		if reason := g.Get("finishReason").String(); reason != "" {
			r.finish = commandCodeFinish(reason)
		}
		if u := g.Get("usage"); u.IsObject() {
			us := readCommandCodeUsage(u)
			r.usage = &us
		}
		return nil
	case "finish":
		reason := r.finish
		if reason == "" {
			reason = commandCodeFinish(firstString(g, "finishReason"))
		}
		r.finish = reason
		ch := Chunk{FinishReason: FinishWithCalls(reason, r.calls)}
		if u := g.Get("totalUsage"); u.IsObject() {
			us := readCommandCodeUsage(u)
			ch.Usage = &us
		} else if r.usage != nil {
			us := *r.usage
			ch.Usage = &us
		}
		return r.emit(ch)
	case "error":
		text := ""
		if e := g.Get("error"); e.Exists() {
			if e.Type == gjson.String {
				text = e.String()
			} else {
				text = e.Get("message").String()
			}
		}
		if text == "" {
			text = g.Get("message").String()
		}
		return []Chunk{{Err: &ChunkError{Message: commandCodeError(text)}}}
	}
	return nil
}

// emit opens the stream on the first chunk, so a client sees the role before
// any content.
func (r *commandCodeRead) emit(ch Chunk) []Chunk {
	if !r.opened && (ch.Text != "" || ch.Reasoning != "" || len(ch.ToolCalls) > 0) {
		r.opened = true
		ch.Role = RoleAssistant
	}
	if ch.Text == "" && ch.Reasoning == "" && len(ch.ToolCalls) == 0 && ch.FinishReason == "" && ch.Usage == nil {
		return nil
	}
	return []Chunk{ch}
}

// startCall opens a call from its first fragment.
func (r *commandCodeRead) startCall(g gjson.Result) []Chunk {
	id := firstString(g, "id", "toolCallId")
	if id == "" {
		id = fmt.Sprintf("call_cc_%d", r.calls)
	}
	idx, seen := r.index[id]
	if !seen {
		idx = r.calls
		r.index[id] = idx
		r.calls++
	}
	r.streamed[id] = true
	return r.emit(Chunk{ToolCalls: []ToolCallDelta{{
		Index: idx, ID: id, Name: g.Get("toolName").String(),
	}}})
}

// deltaCall adds one fragment of arguments to a call already opened.
func (r *commandCodeRead) deltaCall(g gjson.Result) []Chunk {
	id := firstString(g, "id", "toolCallId")
	idx, ok := r.index[id]
	if !ok {
		return nil
	}
	args := firstString(g, "delta", "inputTextDelta")
	if args == "" {
		return nil
	}
	return []Chunk{{ToolCalls: []ToolCallDelta{{Index: idx, Arguments: args}}}}
}

// wholeCall emits a complete call, unless its fragments already did.
func (r *commandCodeRead) wholeCall(g gjson.Result) []Chunk {
	id := g.Get("toolCallId").String()
	if id == "" {
		id = fmt.Sprintf("call_cc_%d", r.calls)
	}
	if r.streamed[id] {
		return nil
	}
	idx, seen := r.index[id]
	if !seen {
		idx = r.calls
		r.index[id] = idx
		r.calls++
	}
	r.streamed[id] = true
	return r.emit(Chunk{ToolCalls: []ToolCallDelta{{
		Index: idx, ID: id, Name: g.Get("toolName").String(), Arguments: argumentsJSON(g.Get("input")),
	}}})
}

// argumentsJSON is a call's input as the JSON text a client parses.
func argumentsJSON(in gjson.Result) string {
	switch {
	case !in.Exists():
		return "{}"
	case in.Type == gjson.String:
		if gjson.Valid(in.String()) {
			return in.String()
		}
		return "{}"
	}
	return in.Raw
}

// readCommandCodeUsage maps the counts the AI SDK reports.
func readCommandCodeUsage(u gjson.Result) Usage {
	return Usage{
		Prompt:     u.Get("inputTokens").Int(),
		Completion: u.Get("outputTokens").Int(),
		Cached:     u.Get("cachedInputTokens").Int(),
		Reasoning:  u.Get("reasoningTokens").Int(),
	}
}

// commandCodeFinish maps the AI SDK's reason to the hub's.
func commandCodeFinish(reason string) string {
	switch reason {
	case "tool-calls", "tool_use":
		return FinishToolCalls
	case "length", "max_tokens":
		return FinishLength
	case "content-filter", "content_filter":
		return FinishFilter
	}
	return FinishStop
}

// commandCodeError is the fixed text for an error the upstream reported. Its
// own words are never repeated, because they may carry the body or an address.
func commandCodeError(string) string { return "the upstream reported an error" }

// NewStreamWriter returns the writer for CommandCode streams. Nothing writes
// this format: no client speaks it, so a request is never answered in it.
func (CommandCodeCodec) NewStreamWriter(c *Ctx) StreamWriter { return commandCodeNoWriter{} }

type commandCodeNoWriter struct{}

func (commandCodeNoWriter) Write(Chunk) []Frame { return nil }
func (commandCodeNoWriter) Held() bool          { return false }
func (commandCodeNoWriter) Close() []Frame      { return nil }
