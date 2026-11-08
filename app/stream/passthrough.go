package stream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/translate"
)

// passthrough forwards an upstream that already speaks the client's format.
// Frames keep their event names. Only the repairs a strict client needs are
// made, and the usage is still read for the stored row.
func (r *runner) passthrough(ctx context.Context) {
	reader := newTextReader(r.j.UpstreamFormat)
	n := &normalizer{seen: map[int64]bool{}}
	terminal := false
	for {
		ev, err := r.pull.next(ctx, nil)
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
		} else if !gjson.ValidBytes(data) {
			continue
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
	if !gjson.ValidBytes(data) || !gjson.ParseBytes(data).IsObject() {
		return nil, false
	}
	out := data
	if gjson.GetBytes(out, "choices").Exists() {
		if id := gjson.GetBytes(out, "id").String(); id == "" || id == "chat" || id == "completion" || len(id) < 8 {
			if n.id == "" {
				var b [8]byte
				_, _ = rand.Read(b[:])
				n.id = "chatcmpl-" + hex.EncodeToString(b[:])
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
		out, _ = sjson.DeleteBytes(out, fmt.Sprintf("choices.%d.content_filter_results", i))
		tc := gjson.GetBytes(out, fmt.Sprintf("choices.%d.delta.tool_calls", i))
		if tc.IsArray() && len(tc.Array()) == 0 {
			out, _ = sjson.DeleteBytes(out, fmt.Sprintf("choices.%d.delta.tool_calls", i))
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
	path := fmt.Sprintf("choices.%d.delta.tool_calls", choice)
	for i, tc := range gjson.GetBytes(b, path).Array() {
		idx := tc.Get("index").Int()
		if id := tc.Get("id").String(); id != "" {
			n.ids[idx] = id
			continue
		}
		if _, open := n.ids[idx]; !open {
			id := fmt.Sprintf("call_stream_%d", idx)
			n.ids[idx] = id
			b, _ = sjson.SetBytes(b, fmt.Sprintf("%s.%d.id", path, i), id)
		}
	}
	return b
}

// worthSending is false for a chunk that carries nothing: no role, content,
// reasoning, tool call, finish reason or usage.
func worthSending(b []byte) bool {
	g := gjson.ParseBytes(b)
	if !g.Get("choices").Exists() {
		return true
	}
	if g.Get("usage").IsObject() || g.Get("error").Exists() {
		return true
	}
	c := g.Get("choices.0")
	d := c.Get("delta")
	return d.Get("content").String() != "" || d.Get("role").Exists() || c.Get("finish_reason").String() != "" ||
		d.Get("tool_calls").Exists() || d.Get("reasoning_content").String() != "" || d.Get("reasoning").String() != "" ||
		d.Get("reasoning_details").Exists() || !d.Exists()
}

// textReader pulls the text length and the finish out of passthrough events so
// the row has the same facts a translated stream would give.
type textReader struct{ format string }

func newTextReader(format string) *textReader { return &textReader{format: format} }

func (t *textReader) count(tr *Tracker, data []byte) {
	g := gjson.ParseBytes(data)
	switch t.format {
	case translate.FormatOpenAI:
		d := g.Get("choices.0.delta")
		tr.AddText(len(d.Get("content").String()) + len(d.Get("reasoning_content").String()))
	case translate.FormatClaude:
		d := g.Get("delta")
		tr.AddText(len(d.Get("text").String()) + len(d.Get("thinking").String()))
	case translate.FormatResponses:
		if typ := g.Get("type").String(); typ == "response.output_text.delta" || typ == "response.reasoning_summary_text.delta" {
			tr.AddText(len(g.Get("delta").String()))
		}
	case translate.FormatGemini:
		for _, p := range g.Get("candidates.0.content.parts").Array() {
			tr.AddText(len(p.Get("text").String()))
		}
	case translate.FormatOllama:
		tr.AddText(len(g.Get("message.content").String()) + len(g.Get("message.thinking").String()))
	}
}

func (t *textReader) finish(data []byte) string {
	g := gjson.ParseBytes(data)
	switch t.format {
	case translate.FormatOpenAI:
		return g.Get("choices.0.finish_reason").String()
	case translate.FormatClaude:
		return translate.FinishFromClaude(g.Get("delta.stop_reason").String())
	case translate.FormatGemini:
		if f := g.Get("candidates.0.finishReason").String(); f != "" {
			return translate.FinishFromGemini(f)
		}
	case translate.FormatOllama:
		if g.Get("done").Bool() {
			return g.Get("done_reason").String()
		}
	}
	return ""
}

// failed reports an error event inside the stream.
func (t *textReader) failed(data []byte) bool {
	g := gjson.ParseBytes(data)
	switch t.format {
	case translate.FormatClaude:
		return g.Get("type").String() == "error"
	case translate.FormatResponses:
		return g.Get("type").String() == "response.failed"
	case translate.FormatOllama:
		return g.Get("error").Exists() && !g.Get("message").Exists()
	}
	return g.Get("error").IsObject()
}
