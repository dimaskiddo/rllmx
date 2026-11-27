package stream

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/llm/translate"
)

// passthrough forwards an upstream that already speaks the client's format.
// Frames keep their event names. Only the repairs a strict client needs are
// made, and the usage is still read for the stored row.
func (r *runner) passthrough(ctx context.Context) {
	reader := newTextReader(r.j.UpstreamFormat)
	n := &normalizer{seen: map[int64]bool{}}
	terminal := false
	for {
		ev, err := r.pull.next(ctx, nil, r.keepC())
		if errors.Is(err, errKeepalive) {
			if !r.keepalive() {
				return
			}
			r.resetKeep()
			continue
		}
		r.resetKeep()
		if errors.Is(err, io.EOF) {
			r.endPassthrough(terminal)
			return
		}
		if err != nil {
			r.fail(err, ReasonText(err))
			return
		}
		r.sawEvent()
		data := ev.Data
		if r.j.UpstreamFormat == translate.FormatOpenAI {
			var keep bool
			data, keep = n.fix(data)
			if !keep {
				continue
			}
			if r.j.ReasoningAliasFields {
				data = liftReasoning(data)
			}
		} else if !gjson.ValidBytes(data) {
			continue
		}
		if r.j.Rewrite != nil {
			data = r.j.Rewrite(ev.Name, data)
		}
		r.track.Feed(data)
		reader.count(r.track, data)
		if reader.failed(data) {
			r.fail(ErrUpstream, "The upstream stream ended with an error")
			return
		}
		if f := reader.finish(data); f != "" {
			r.finish = f
		}
		if r.j.UpstreamFormat == translate.FormatResponses && isResponsesTerminal(ev.Name, data) {
			terminal = true
		}
		if !r.send([]translate.Frame{{Event: ev.Name, Data: data}}) {
			return
		}
	}
}

// endPassthrough closes a stream whose upstream ended. A Responses stream with
// no terminal event is failed, so the client does not take a cut-off answer for
// a finished one. OpenAI streams end with one [DONE]; Gemini and Ollama have no
// end marker.
func (r *runner) endPassthrough(terminal bool) {
	switch r.j.UpstreamFormat {
	case translate.FormatResponses:
		if !terminal {
			r.fail(ErrUpstream, "The upstream stream ended before it finished")
		}
		r.send([]translate.Frame{{Done: true}})
	case translate.FormatOpenAI:
		r.send([]translate.Frame{{Done: true}})
	}
}

// isResponsesTerminal reports the events after which a Responses stream is over.
func isResponsesTerminal(name string, data []byte) bool {
	typ := gjson.GetBytes(data, "type").String()
	if typ == "" {
		typ = name
	}
	switch typ {
	case "response.completed", "response.done", "response.failed", "error":
		return true
	}
	return false
}

// normalizer repairs Chat Completions chunks for strict clients.
type normalizer struct {
	id   string
	seen map[int64]bool
	ids  map[int64]string
}

// fix returns a repaired chunk and false when the line is not one worth
// forwarding: not JSON, or an empty delta.
func (n *normalizer) fix(data []byte) ([]byte, bool) {
	if !gjson.ValidBytes(data) || !translate.IsObject(data) {
		return nil, false
	}
	out := data
	if gjson.GetBytes(out, "choices").Exists() {
		if translate.WeakStreamID(gjson.GetBytes(out, "id").String()) {
			if n.id == "" {
				n.id = translate.StrictStreamID("")
			}
			out, _ = sjson.SetBytes(out, "id", n.id)
		}
		if !gjson.GetBytes(out, "object").Exists() {
			out, _ = sjson.SetBytes(out, "object", "chat.completion.chunk")
		}
		if gjson.GetBytes(out, "created").Int() == 0 {
			out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
		}
	}
	out, _ = sjson.DeleteBytes(out, "prompt_filter_results")
	for i := range gjson.GetBytes(out, "choices").Array() {
		out, _ = sjson.DeleteBytes(out, choicePath(i, "content_filter_results"))
		tcPath := choicePath(i, "delta.tool_calls")
		tc := gjson.GetBytes(out, tcPath)
		if tc.IsArray() && len(tc.Array()) == 0 {
			out, _ = sjson.DeleteBytes(out, tcPath)
		}
		out = n.toolIDs(out, i)
	}
	if !worthSending(out) {
		return nil, false
	}
	return out, true
}

// toolIDs gives a fragment that opens a tool-call index without an id one, and
// keeps it for the rest of the call.
func (n *normalizer) toolIDs(b []byte, choice int) []byte {
	if n.ids == nil {
		n.ids = map[int64]string{}
	}
	path := choicePath(choice, "delta.tool_calls")
	for i, tc := range gjson.GetBytes(b, path).Array() {
		idx := tc.Get("index").Int()
		if id := tc.Get("id").String(); id != "" {
			n.ids[idx] = id
			continue
		}
		if _, open := n.ids[idx]; !open {
			id := "call_stream_" + strconv.FormatInt(idx, 10)
			n.ids[idx] = id
			b, _ = sjson.SetBytes(b, path+"."+strconv.Itoa(i)+".id", id)
		}
	}
	return b
}

// choicePath names a field of one choice. Small indexes come from strconv's
// static table, so the common case allocates only the joined string.
func choicePath(choice int, rest string) string {
	return "choices." + strconv.Itoa(choice) + "." + rest
}

// worthSending is false for a chunk that carries nothing: no role, content,
// reasoning, tool call, finish reason or usage.
func worthSending(b []byte) bool {
	if !gjson.GetBytes(b, "choices").Exists() {
		return true
	}
	if gjson.GetBytes(b, "usage").IsObject() || gjson.GetBytes(b, "error").Exists() {
		return true
	}
	c := gjson.GetBytes(b, "choices.0")
	d := c.Get("delta")
	return d.Get("content").String() != "" || d.Get("role").Exists() || c.Get("finish_reason").String() != "" ||
		d.Get("tool_calls").Exists() || d.Get("reasoning_content").String() != "" || d.Get("reasoning").String() != "" ||
		d.Get("reasoning_details").Exists() || !d.Exists()
}

// liftReasoning copies a string `reasoning` into `reasoning_content` on each
// choice's delta that has none, for a client that reads only the second name. A
// `reasoning` that is not a string is left as it came.
func liftReasoning(data []byte) []byte {
	for i := range gjson.GetBytes(data, "choices").Array() {
		prefix := choicePath(i, "delta")
		if gjson.GetBytes(data, prefix+".reasoning_content").Exists() {
			continue
		}
		if v := gjson.GetBytes(data, prefix+".reasoning"); v.Type == gjson.String && v.String() != "" {
			data, _ = sjson.SetBytes(data, prefix+".reasoning_content", v.String())
		}
	}
	return data
}

// textReader pulls the text length and the finish out of passthrough events so
// the row has the same facts a translated stream would give.
type textReader struct{ format string }

func newTextReader(format string) *textReader { return &textReader{format: format} }

// The readers below look each path up with GetBytes, which scans the event in
// place; ParseBytes would copy the whole event for every call.

func (t *textReader) count(tr *Tracker, data []byte) {
	get := func(path string) gjson.Result { return gjson.GetBytes(data, path) }
	switch t.format {
	case translate.FormatOpenAI:
		tr.AddText(len(get("choices.0.delta.content").String()) + len(get("choices.0.delta.reasoning_content").String()))
	case translate.FormatClaude:
		tr.AddText(len(get("delta.text").String()) + len(get("delta.thinking").String()))
	case translate.FormatResponses:
		if typ := get("type").String(); typ == "response.output_text.delta" || typ == "response.reasoning_summary_text.delta" {
			tr.AddText(len(get("delta").String()))
		}
	case translate.FormatGemini, translate.FormatCloudCode, translate.FormatVertex:
		for _, p := range get("candidates.0.content.parts").Array() {
			tr.AddText(len(p.Get("text").String()))
		}
	case translate.FormatOllama:
		tr.AddText(len(get("message.content").String()) + len(get("message.thinking").String()))
	}
}

func (t *textReader) finish(data []byte) string {
	get := func(path string) gjson.Result { return gjson.GetBytes(data, path) }
	switch t.format {
	case translate.FormatOpenAI:
		return get("choices.0.finish_reason").String()
	case translate.FormatClaude:
		return translate.FinishFromClaude(get("delta.stop_reason").String())
	case translate.FormatGemini, translate.FormatCloudCode, translate.FormatVertex:
		if f := get("candidates.0.finishReason").String(); f != "" {
			return translate.FinishFromGemini(f)
		}
	case translate.FormatOllama:
		if get("done").Bool() {
			return get("done_reason").String()
		}
	}
	return ""
}

// failed reports an error event inside the stream.
func (t *textReader) failed(data []byte) bool {
	get := func(path string) gjson.Result { return gjson.GetBytes(data, path) }
	switch t.format {
	case translate.FormatClaude:
		return get("type").String() == "error"
	case translate.FormatResponses:
		return get("type").String() == "response.failed"
	case translate.FormatOllama:
		return get("error").Exists() && !get("message").Exists()
	}
	return get("error").IsObject()
}
