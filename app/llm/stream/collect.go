package stream

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"

	"rllmx/app/llm/translate"
)

// ErrStreamFailed means the upstream reported an error inside the stream, or
// the stream broke before it finished.
var ErrStreamFailed = errors.New("stream: the upstream stream failed")

// Collected is a whole answer read from a stream, with what the row needs.
type Collected struct {
	Response translate.Response
	Usage    translate.Usage
	Finish   string
	// ContentLen is the streamed text length, the basis of an estimate.
	ContentLen int
}

// MaxCollected bounds what Collect adds up from one stream, so an upstream that
// trickles forever cannot grow the buffer without end. It matches the cap on a
// non-stream answer.
const MaxCollected = 64 << 20

// ErrCollectedTooLarge means a stream added up to more than MaxCollected.
var ErrCollectedTooLarge = errors.New("stream: the upstream answer is too large")

type callBuf struct {
	translate.ToolCall
	args strings.Builder
}

// Collect reads an upstream stream to its end and returns the one answer it
// adds up to. It serves a client that asked for JSON from a provider that only
// streams. Only the hub is touched, so it works for every upstream format.
func Collect(ctx context.Context, body io.ReadCloser, src Source, format string, c *translate.Ctx, t Timers, requestBytes int) (Collected, error) {
	return collectUpTo(ctx, body, src, format, c, t, requestBytes, MaxCollected)
}

func collectUpTo(ctx context.Context, body io.ReadCloser, src Source, format string, c *translate.Ctx, t Timers, requestBytes, limit int) (Collected, error) {
	codec, ok := translate.Lookup(format)
	if !ok {
		_ = body.Close()
		return Collected{}, ErrStreamFailed
	}
	reader := codec.NewStreamReader(c)
	p := newPull(body, src, t)
	defer p.stop()
	track := NewTracker(format)

	var out Collected
	var text, reasoning, signature strings.Builder
	calls := map[int]*callBuf{}
	total := 0
	absorb := func(ch translate.Chunk) error {
		if ch.Err != nil {
			return ErrStreamFailed
		}
		total += len(ch.Text) + len(ch.Reasoning) + len(ch.Signature)
		for _, d := range ch.ToolCalls {
			total += len(d.Arguments)
		}
		if total > limit {
			return ErrCollectedTooLarge
		}
		if ch.ID != "" && out.Response.ID == "" {
			out.Response.ID = ch.ID
		}
		if ch.Model != "" && out.Response.Model == "" {
			out.Response.Model = ch.Model
		}
		if ch.Created != 0 && out.Response.Created == 0 {
			out.Response.Created = ch.Created
		}
		text.WriteString(ch.Text)
		reasoning.WriteString(ch.Reasoning)
		track.AddText(len(ch.Text) + len(ch.Reasoning))
		signature.WriteString(ch.Signature)
		out.Response.Message.Content = append(out.Response.Message.Content, ch.Images...)
		for _, d := range ch.ToolCalls {
			cb := calls[d.Index]
			if cb == nil {
				cb = &callBuf{}
				calls[d.Index] = cb
			}
			if d.ID != "" {
				cb.ID = d.ID
			}
			if d.Name != "" {
				cb.Name = d.Name
			}
			if d.Signature != "" {
				cb.Signature = d.Signature
			}
			cb.Custom = cb.Custom || d.Custom
			cb.args.WriteString(d.Arguments)
		}
		if ch.FinishReason != "" {
			out.Finish = ch.FinishReason
		}
		return nil
	}
	for {
		ev, err := p.next(ctx, nil, nil)
		if errors.Is(err, io.EOF) {
			for _, ch := range reader.Flush() {
				if err := absorb(ch); err != nil {
					return Collected{}, err
				}
			}
			break
		}
		if err != nil {
			return Collected{}, err
		}
		track.Feed(ev.Data)
		for _, ch := range reader.Read(ev.Name, ev.Data) {
			if err := absorb(ch); err != nil {
				return Collected{}, err
			}
		}
	}

	msg := &out.Response.Message
	msg.Role = translate.RoleAssistant
	msg.Reasoning = reasoning.String()
	msg.ReasoningSignature = signature.String()
	if text.Len() > 0 {
		msg.Content = append(translate.TextPart(text.String()), msg.Content...)
	}
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		call := calls[i].ToolCall
		call.Arguments = calls[i].args.String()
		if call.Arguments == "" {
			call.Arguments = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, call)
	}
	out.Finish = translate.FinishWithCalls(out.Finish, len(msg.ToolCalls))
	out.Usage = track.Final(requestBytes)
	out.ContentLen = track.ContentLen()
	out.Response.FinishReason = out.Finish
	out.Response.Usage = out.Usage
	return out, nil
}
