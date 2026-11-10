package kiro

import (
	"strconv"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/translate"
	"rllmx/app/store/document"
)

// kiroStreamRead reads CodeWhisperer's events. Text arrives as
// assistantResponseEvent and codeEvent, thinking as reasoningContentEvent, tool
// calls as toolUseEvent fragments the reader buffers until the turn ends.
//
// The buffering is why the stream is not passed straight on: a fragment that
// carries half an arguments object is not something a client can act on, and
// the reference hardened this path against exactly that.
type kiroStreamRead struct {
	c      *translate.Ctx
	opened map[string]*kiroOpenCall
	order  []string
	// buffered counts the tool-call bytes held, which is the one structure that
	// grows from input.
	buffered int
	tools    int
	finish   bool
	// thinking tracks a <thinking> block split across two events.
	thinking bool
	// usage is the last report, merged field by field.
	usage    translate.Usage
	hasUsage bool
}

type kiroOpenCall struct {
	id, name string
	args     strings.Builder
	bytes    int
}

// NewStreamReader returns the reader for CodeWhisperer streams.
func (Codec) NewStreamReader(c *translate.Ctx) translate.StreamReader {
	return &kiroStreamRead{c: c, opened: map[string]*kiroOpenCall{}}
}

// Flush hands on whatever the turn left buffered. The upstream names its stop
// reason before the turn is over, so a call still held at the end is one the
// stream meant to send.
func (r *kiroStreamRead) Flush() []translate.Chunk { return r.release() }

// Read handles one event. Its name is the frame's own; a payload that repeats
// it is read the same way.
func (r *kiroStreamRead) Read(event string, data []byte) []translate.Chunk {
	if !gjson.ValidBytes(data) || !gjson.ParseBytes(data).IsObject() {
		return nil
	}
	g := gjson.ParseBytes(data)
	name := event
	if name == "" {
		name = g.Get("_eventType").String()
	}
	switch name {
	case "assistantResponseEvent":
		return r.content(g.Get("content").String())
	case "codeEvent":
		if t := g.Get("content").String(); t != "" {
			return []translate.Chunk{{Text: t}}
		}
	case "reasoningContentEvent":
		if t := document.FirstString(g.Get("text"), g.Get("content")); t != "" {
			return []translate.Chunk{{Reasoning: t}}
		}
	case "toolUseEvent":
		return r.toolUse(data)
	case "messageStopEvent":
		r.finish = true
		return []translate.Chunk{{FinishReason: translate.FinishWithCalls(translate.FinishStop, r.tools)}}
	case "metadataEvent", "MetadataEvent":
		if reason := g.Get("stopReason").String(); reason != "" {
			r.finish = true
			return []translate.Chunk{{FinishReason: translate.FinishWithCalls(kiroFinish(reason), r.tools)}}
		}
	case "metricsEvent":
		return r.metrics(g)
	case "error":
		return []translate.Chunk{{Err: translate.ReadErrorObject(g.Get("error"))}}
	}
	return nil
}

// content emits text, splitting a <thinking> block out of it. The upstream
// sometimes wraps its reasoning in those tags instead of sending
// reasoningContentEvent, and a client that shows them as text shows its own
// scaffolding.
func (r *kiroStreamRead) content(text string) []translate.Chunk {
	if text == "" {
		return nil
	}
	var out []translate.Chunk
	for text != "" {
		if r.thinking {
			end := strings.Index(text, "</thinking>")
			if end < 0 {
				out = append(out, translate.Chunk{Reasoning: text})
				break
			}
			out = append(out, translate.Chunk{Reasoning: text[:end]})
			r.thinking = false
			text = strings.TrimPrefix(text[end+len("</thinking>"):], "\n")
			continue
		}
		start := strings.Index(text, "<thinking>")
		if start < 0 {
			out = append(out, translate.Chunk{Text: text})
			break
		}
		if start > 0 {
			out = append(out, translate.Chunk{Text: text[:start]})
		}
		r.thinking = true
		text = text[start+len("<thinking>"):]
	}
	return out
}

// toolUse buffers one event's fragments. A call is opened by its id and grows
// while the same id keeps arriving; its arguments are joined and only handed
// on once the turn ends.
func (r *kiroStreamRead) toolUse(data []byte) []translate.Chunk {
	for _, v := range gjson.GetBytes(data, "toolUseEvent").Array() {
		if !v.Exists() {
			// The payload is the event itself when it holds no wrapper.
			v = gjson.ParseBytes(data)
		}
		if !v.IsObject() {
			continue
		}
		id := v.Get("toolUseId").String()
		name := strings.TrimSpace(v.Get("name").String())
		if id == "" && name == "" {
			continue
		}
		call := r.opened[id]
		if call == nil {
			if name == "" {
				continue
			}
			call = &kiroOpenCall{id: id, name: r.c.RestoreTool(name)}
			if call.id == "" {
				call.id = "call_" + strconv.Itoa(len(r.order)+1)
			}
			r.opened[call.id] = call
			r.order = append(r.order, call.id)
		}
		r.appendArgs(call, v.Get("input"))
	}
	return nil
}

// appendArgs adds one fragment of a call's arguments, bailing out rather than
// growing without bound.
func (r *kiroStreamRead) appendArgs(call *kiroOpenCall, input gjson.Result) {
	if !input.Exists() {
		return
	}
	var fragment string
	switch {
	case input.Type == gjson.String:
		fragment = input.String()
	case input.IsObject():
		// A whole object replaces what came before: the upstream sends the same
		// call as an object on some turns and as text fragments on others.
		fragment = input.Raw
		call.args.Reset()
		call.bytes = 0
	default:
		return
	}
	if r.buffered+len(fragment) > kiroToolBufferMax {
		return
	}
	call.args.WriteString(fragment)
	call.bytes += len(fragment)
	r.buffered += len(fragment)
}

// release hands the buffered calls on as deltas. Each one opens with its id and
// name, then carries its whole argument text.
func (r *kiroStreamRead) release() []translate.Chunk {
	if len(r.order) == 0 {
		return nil
	}
	out := make([]translate.Chunk, 0, len(r.order))
	for i, id := range r.order {
		call := r.opened[id]
		args := strings.TrimSpace(call.args.String())
		if args == "" {
			args = "{}"
		}
		out = append(out, translate.Chunk{ToolCalls: []translate.ToolCallDelta{{
			Index: i, ID: call.id, Name: call.name, Arguments: args,
		}}})
		r.tools++
	}
	r.order = r.order[:0]
	r.opened = map[string]*kiroOpenCall{}
	r.buffered = 0
	return out
}

// metrics reads the turn's token counts.
func (r *kiroStreamRead) metrics(g gjson.Result) []translate.Chunk {
	u := translate.Usage{Prompt: g.Get("inputTokens").Int(), Completion: g.Get("outputTokens").Int()}
	u.Cached = max(g.Get("cacheReadInputTokens").Int(), g.Get("cache_read_input_tokens").Int())
	u.CacheWrite = max(g.Get("cacheCreationInputTokens").Int(), g.Get("cache_creation_input_tokens").Int())
	if u.Prompt == 0 && u.Completion == 0 {
		return nil
	}
	r.usage, r.hasUsage = translate.MergeUsage(r.usage, u), true
	return []translate.Chunk{{Usage: &u}}
}

// kiroFinish maps the upstream's stop reason onto the hub's.
func kiroFinish(reason string) string {
	switch reason {
	case "tool_use", "tool_calls":
		return translate.FinishToolCalls
	case "max_tokens", "model_context_window_exceeded", "length":
		return translate.FinishLength
	}
	return translate.FinishStop
}

// kiroToolBufferMax bounds what the reader holds while waiting for a turn to
// end. It is the protocol's own ceiling, not a tuning knob.
const kiroToolBufferMax = 8 << 20

// NewStreamWriter returns the writer for CodeWhisperer streams. Nothing writes
// this format: no client speaks it, so a request is never answered in it.
func (Codec) NewStreamWriter(c *translate.Ctx) translate.StreamWriter { return kiroNoWriter{} }

type kiroNoWriter struct{}

func (kiroNoWriter) Write(translate.Chunk) []translate.Frame { return nil }
func (kiroNoWriter) Held() bool                              { return false }
func (kiroNoWriter) Close() []translate.Frame                { return nil }
