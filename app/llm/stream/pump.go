package stream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/failure"
)

// Client is where a stream goes: an SSE or NDJSON writer.
type Client interface {
	Frame(translate.Frame) error
	Raw([]byte) error
}

// Job is one stream to run.
type Job struct {
	// Body is the upstream body; Run closes it.
	Body io.ReadCloser
	// Source frames the body: NewReader for SSE, NewLines for NDJSON.
	Source Source
	// UpstreamFormat and ClientFormat are codec names. The same name selects
	// passthrough.
	UpstreamFormat string
	ClientFormat   string
	Ctx            *translate.Ctx
	Client         Client
	Timers         Timers
	// RequestBytes sizes the prompt estimate when the upstream reports none.
	RequestBytes int
	// Buffer adds the client buffer to usage objects sent to the client.
	Buffer bool
	// Rewrite changes an upstream event before a passthrough sends it on; nil
	// leaves every event as it came. Only the passthrough path uses it, because
	// a translated stream goes through the codec, which has its own hook.
	Rewrite func(event string, data []byte) []byte
	// Now is the clock for the first-byte time; nil uses time.Now.
	Now func() time.Time
	// MaxBytes caps what a translated stream may add up to; zero is MaxCollected.
	MaxBytes int
	// ReasoningAliasFields copies a string `reasoning` into `reasoning_content`
	// on each passed-through OpenAI delta that carries none of its own.
	ReasoningAliasFields bool
}

// Result is how a stream ended. Wrote tells whether any byte reached the
// client, which decides whether a failover is still possible.
type Result struct {
	Usage      translate.Usage
	Finish     string
	ContentLen int
	TTFT       time.Duration
	Wrote      bool
	Err        error
	// Status is the status the failure frame carried, 0 when the stream ended well.
	Status int
}

// Run moves an upstream stream to the client until it ends, fails or stalls.
// It always closes the upstream, so a departed client cancels the request.
func Run(ctx context.Context, j Job) Result {
	if j.Now == nil {
		j.Now = time.Now
	}
	r := &runner{j: j, track: NewTracker(j.UpstreamFormat), start: j.Now(), pull: newPull(j.Body, j.Source, j.Timers)}
	defer r.pull.stop()
	if j.UpstreamFormat == j.ClientFormat {
		r.passthrough(ctx)
	} else {
		r.translate(ctx)
	}
	return r.result()
}

type runner struct {
	j      Job
	track  *Tracker
	pull   *pull
	start  time.Time
	ttft   time.Duration
	wrote  bool
	finish string
	err    error
	status int
	// streamed counts the text, reasoning and tool-call bytes the codecs were
	// given, which is what their writers keep until the stream ends.
	streamed int
}

func (r *runner) limit() int {
	if r.j.MaxBytes > 0 {
		return r.j.MaxBytes
	}
	return MaxCollected
}

func (r *runner) result() Result {
	return Result{
		Usage: r.track.Final(r.j.RequestBytes), Finish: r.finish, ContentLen: r.track.ContentLen(),
		TTFT: r.ttft, Wrote: r.wrote, Err: r.err, Status: r.status,
	}
}

func (r *runner) sawEvent() {
	if r.ttft == 0 {
		r.ttft = max(r.j.Now().Sub(r.start), 1)
	}
}

// send writes frames, adding the client buffer to usage objects on the way.
func (r *runner) send(frames []translate.Frame) bool {
	for _, f := range frames {
		if r.j.Buffer && !f.Done {
			f.Data = AddClientBuffer(r.j.ClientFormat, f.Data)
		}
		if err := r.j.Client.Frame(f); err != nil {
			r.err = ErrClientGone
			return false
		}
		r.wrote = true
	}
	return true
}

// fail ends the stream with the client's own error frame. The status is 504
// only for a stall, and 502 for everything else that breaks a stream.
func (r *runner) fail(cause error, msg string) {
	status := http.StatusBadGateway
	if errors.Is(cause, ErrStall) || errors.Is(cause, ErrFirstChunk) {
		status = http.StatusGatewayTimeout
	}
	r.err, r.status = cause, status
	if errors.Is(cause, ErrClientGone) {
		return
	}
	frame := failure.Frame(failure.ForClient(r.j.ClientFormat), status, msg)
	if len(frame) == 0 {
		return
	}
	if r.j.Client.Raw(frame) == nil {
		r.wrote = true
	}
}

// ReasonText is the fixed message a client sees for each way a stream breaks.
// It never repeats upstream text.
func ReasonText(cause error) string {
	switch {
	case errors.Is(cause, ErrStall):
		return "The upstream stopped sending data"
	case errors.Is(cause, ErrFirstChunk):
		return "The upstream did not start answering in time"
	case errors.Is(cause, ErrEventTooLarge):
		return "The upstream sent an event that is too large"
	case errors.Is(cause, ErrCollectedTooLarge):
		return "The upstream answer is too large"
	}
	return "The upstream stream ended with an error"
}

func (r *runner) translate(ctx context.Context) {
	up, ok := translate.Lookup(r.j.UpstreamFormat)
	down, ok2 := translate.Lookup(r.j.ClientFormat)
	if !ok || !ok2 {
		r.fail(ErrUpstream, "The upstream stream ended with an error")
		return
	}
	reader := up.NewStreamReader(r.j.Ctx)
	writer := down.NewStreamWriter(r.j.Ctx)
	var usageDeadline <-chan time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		ev, err := r.pull.next(ctx, usageDeadline)
		if errors.Is(err, errUsageWait) {
			r.send(writer.Close())
			return
		}
		if errors.Is(err, io.EOF) {
			for _, ch := range reader.Flush() {
				if !r.chunk(writer, ch) {
					return
				}
			}
			r.send(writer.Close())
			return
		}
		if err != nil {
			r.fail(err, ReasonText(err))
			return
		}
		r.sawEvent()
		r.track.Feed(ev.Data)
		for _, ch := range reader.Read(ev.Name, ev.Data) {
			if ch.Err != nil {
				r.fail(ErrUpstream, "The upstream stream ended with an error")
				return
			}
			if !r.chunk(writer, ch) {
				return
			}
		}
		if writer.Held() && usageDeadline == nil && r.j.Timers.UsageWait > 0 {
			timer = time.NewTimer(r.j.Timers.UsageWait)
			usageDeadline = timer.C
		}
	}
}

// chunk counts one hub chunk and writes what the writer makes of it.
func (r *runner) chunk(w translate.StreamWriter, ch translate.Chunk) bool {
	r.streamed += len(ch.Text) + len(ch.Reasoning) + len(ch.Signature)
	for _, d := range ch.ToolCalls {
		r.streamed += len(d.Arguments)
	}
	if r.streamed > r.limit() {
		r.fail(ErrCollectedTooLarge, ReasonText(ErrCollectedTooLarge))
		return false
	}
	r.track.AddText(len(ch.Text) + len(ch.Reasoning))
	if ch.FinishReason != "" {
		r.finish = ch.FinishReason
	}
	return r.send(w.Write(ch))
}
