package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"rllmx/app/stream"
	"rllmx/app/translate"
)

// What the integrity gate holds and how long it waits. The buffer is the
// protocol's own ceiling; the waits are the stream's, so a slow first byte and
// a stalled stream end the same way they do on any other provider.
const kiroGateBytes = 8 << 20

// kiroVerdict is what the gate made of one answer.
type kiroVerdict int

const (
	// kiroComplete is an answer worth sending.
	kiroComplete kiroVerdict = iota
	// kiroRepairable is an answer that ended on nothing: an ellipsis, a promise
	// to check something, or a tool call the client cannot act on.
	kiroRepairable
	// kiroBroken is a stream that failed, so asking again would only fail again.
	kiroBroken
)

// kiroAttempt is one answer as the gate read it.
type kiroAttempt struct {
	verdict kiroVerdict
	// body is the client-ready events, kept only for a complete answer.
	body []byte
	// reason says what was wrong, in the reference's own words, for the repair
	// instruction and for the client when the second attempt fails too.
	reason string
}

// gate reads a streamed answer whole and reports what it is, so a turn that
// ended on nothing can be asked for again. The events are turned back into
// what the pipeline reads before they leave, so nothing downstream knows a
// binary stream was ever involved.
func (k *Kiro) gate(ctx context.Context, resp Response, reason func() string) (kiroAttempt, error) {
	out, err := readKiroStream(ctx, resp.Body, reason)
	_ = resp.Body.Close()
	if err != nil {
		return kiroAttempt{}, err
	}
	return out, nil
}

// readKiroStream reads frames into the client's events and judges the turn.
func readKiroStream(ctx context.Context, body io.Reader, reason func() string) (kiroAttempt, error) {
	src := stream.NewFrames(body, stream.FramesKiro)
	reader := translate.KiroCodec{}.NewStreamReader(translate.NewCtx(translate.FormatKiro, translate.FormatOpenAI))

	var events []byte
	var text, reasoning, args bytes.Buffer
	var calls, finishes int
	broken := false
	for {
		ev, err := src.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return kiroAttempt{verdict: kiroBroken, reason: reason()}, nil
		}
		events = appendEvent(events, ev.Name, ev.Data)
		for _, ch := range reader.Read(ev.Name, ev.Data) {
			if ch.Err != nil {
				broken = true
				continue
			}
			text.WriteString(ch.Text)
			reasoning.WriteString(ch.Reasoning)
			if ch.FinishReason != "" {
				finishes++
			}
			for _, d := range ch.ToolCalls {
				calls++
				args.WriteString(d.Arguments)
			}
		}
		if int64(len(events)) > kiroGateBytes {
			return kiroAttempt{verdict: kiroBroken, reason: reason()}, nil
		}
		if err := ctx.Err(); err != nil {
			return kiroAttempt{}, err
		}
	}
	for _, ch := range reader.Flush() {
		if ch.Err != nil {
			broken = true
			continue
		}
		for _, d := range ch.ToolCalls {
			calls++
			args.WriteString(d.Arguments)
		}
	}
	if broken {
		return kiroAttempt{verdict: kiroBroken}, nil
	}
	if calls == 0 && kiroUnfinished(text.String(), reasoning.String()) {
		return kiroAttempt{verdict: kiroRepairable, reason: kiroRepairFor(text.String(), reasoning.String())}, nil
	}
	if calls > 0 && !validJSONObject(args.Bytes()) {
		return kiroAttempt{verdict: kiroRepairable, reason: kiroRepairTool}, nil
	}
	if finishes == 0 && text.Len() == 0 && reasoning.Len() == 0 && calls == 0 {
		return kiroAttempt{verdict: kiroBroken}, nil
	}
	return kiroAttempt{verdict: kiroComplete, body: events}, nil
}

// appendEvent writes one event in the text/event-stream form the pipeline
// reads, so a buffered answer is the same bytes a live upstream would send.
func appendEvent(dst []byte, name string, data []byte) []byte {
	if name != "" {
		dst = append(dst, "event: "...)
		dst = append(dst, name...)
		dst = append(dst, '\n')
	}
	dst = append(dst, "data: "...)
	dst = append(dst, data...)
	return append(dst, '\n', '\n')
}

// gateResponse hands the buffered events on as a stream body.
func gateResponse(resp Response, body []byte) Response {
	return replyWith(resp, resp.Status, body, "text/event-stream")
}

// validJSONObject reports arguments a client can parse as an object.
func validJSONObject(raw []byte) bool {
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	return gjsonValidObject(raw)
}

// httpStatus is the status an answer failed with, for a repair that may retry.
func httpStatus(resp Response) int {
	if resp.Status == 0 {
		return http.StatusBadGateway
	}
	return resp.Status
}
