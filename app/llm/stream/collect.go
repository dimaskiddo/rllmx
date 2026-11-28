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

	col := &collector{limit: limit, track: track, calls: map[int]*callBuf{}}
	ended := false
	for {
		ev, err := p.next(ctx, nil, nil)
		if errors.Is(err, io.EOF) {
			// Judged before Flush, which makes up a finish the upstream never sent.
			if !ended && col.out.Finish == "" && col.total > 0 {
				return Collected{}, ErrTruncated
			}
			for _, ch := range reader.Flush() {
				if err := col.absorb(ch); err != nil {
					return Collected{}, err
				}
			}
			break
		}
		if err != nil {
			return Collected{}, err
		}
		track.Feed(ev.Data)
		ended = ended || isDone(format, ev.Data)
		for _, ch := range reader.Read(ev.Name, ev.Data) {
			if err := col.absorb(ch); err != nil {
				return Collected{}, err
			}
		}
	}

	return col.finish(requestBytes), nil
}

// collector folds a stream's chunks into one answer.
type collector struct {
	out                        Collected
	text, reasoning, signature strings.Builder
	calls                      map[int]*callBuf
	total, limit               int
	track                      *Tracker
}

func (k *collector) absorb(ch translate.Chunk) error {
	if ch.Err != nil {
		return ErrStreamFailed
	}
	k.total += len(ch.Text) + len(ch.Reasoning) + len(ch.Signature)
	for _, d := range ch.ToolCalls {
		k.total += len(d.Arguments)
	}
	if k.total > k.limit {
		return ErrCollectedTooLarge
	}
	resp := &k.out.Response
	if ch.ID != "" && resp.ID == "" {
		resp.ID = ch.ID
	}
	if ch.Model != "" && resp.Model == "" {
		resp.Model = ch.Model
	}
	if ch.Created != 0 && resp.Created == 0 {
		resp.Created = ch.Created
	}
	k.text.WriteString(ch.Text)
	k.reasoning.WriteString(ch.Reasoning)
	k.track.AddText(len(ch.Text) + len(ch.Reasoning))
	k.signature.WriteString(ch.Signature)
	resp.Message.Content = append(resp.Message.Content, ch.Images...)
	for _, d := range ch.ToolCalls {
		cb := k.calls[d.Index]
		if cb == nil {
			cb = &callBuf{}
			k.calls[d.Index] = cb
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
		k.out.Finish = ch.FinishReason
	}
	return nil
}

// finish builds the answer once the stream has ended.
func (k *collector) finish(requestBytes int) Collected {
	out := k.out
	msg := &out.Response.Message
	msg.Role = translate.RoleAssistant
	msg.Reasoning = k.reasoning.String()
	msg.ReasoningSignature = k.signature.String()
	if k.text.Len() > 0 {
		msg.Content = append(translate.TextPart(k.text.String()), msg.Content...)
	}
	idx := make([]int, 0, len(k.calls))
	for i := range k.calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		call := k.calls[i].ToolCall
		call.Arguments = k.calls[i].args.String()
		if call.Arguments == "" {
			call.Arguments = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, call)
	}
	out.Finish = translate.FinishWithCalls(out.Finish, len(msg.ToolCalls))
	out.Usage = k.track.Final(requestBytes)
	out.ContentLen = k.track.ContentLen()
	out.Response.FinishReason = out.Finish
	out.Response.Usage = out.Usage
	return out
}
