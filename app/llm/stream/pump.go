package stream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/failure"
	"rllmx/app/platform/text"
)

// Client is where a stream goes: an SSE or NDJSON writer.
type Client interface {
	Frame(translate.Frame) error
	Raw([]byte) error
}

// Opener is a client that can open its stream before its first frame, so a
// keepalive can reach a request with no failover left.
type Opener interface {
	Open() error
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
	// Keepalive writes a comment line after that much silence on a stream that
	// is already open, so a client or a proxy does not time it out while the
	// upstream is thinking. Zero sends none; an NDJSON client must leave it 0.
	Keepalive time.Duration
	// OpenWhenQuiet lets the first keepalive tick open the client's stream when
	// no member is left to fail over to. False keeps it closed until the first
	// frame, so a failure can still be answered elsewhere.
	OpenWhenQuiet bool
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
	if j.Keepalive > 0 {
		r.keep = time.NewTimer(j.Keepalive)
		defer r.keep.Stop()
	}
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
	// ended records the end marker an OpenAI stream closes with, which finishes
	// the answer without naming a reason.
	ended bool
	// keep is the quiet-stream timer; nil when the job asked for none.
	keep *time.Timer
}

// KeepaliveFrame is a comment line every SSE reader ignores. It carries no
// event, so a client's parser sees nothing and a proxy sees a byte.
const KeepaliveFrame = ": keepalive\n\n"

// keepC is the keepalive timer's channel, or nil when it is off; a nil channel
// never fires, so both loops can select on it unconditionally.
func (r *runner) keepC() <-chan time.Time {
	if r.keep == nil {
		return nil
	}
	return r.keep.C
}

// resetKeep restarts the quiet-stream timer after a real event.
func (r *runner) resetKeep() {
	if r.keep != nil {
		r.keep.Reset(r.j.Keepalive)
	}
}

// keepalive answers a quiet open stream with one comment line and reports
// whether the client took it.
func (r *runner) keepalive() bool {
	// A closed client takes no raw bytes; when it can be opened, the comment
	// itself is the reason to open it.
	if !r.wrote {
		if o, ok := r.j.Client.(Opener); ok {
			if err := o.Open(); err != nil {
				r.err = ErrClientGone
				return false
			}
		}
	}
	if err := r.j.Client.Raw([]byte(KeepaliveFrame)); err != nil {
		r.err = ErrClientGone
		return false
	}
	// A comment is a byte on the wire, so a failover is no longer possible.
	r.wrote = true
	return true
}

// quiet handles a keepalive tick and reports whether the stream goes on. While
// the client's stream is closed and failover may still answer, the tick is
// skipped: a comment there would open it.
func (r *runner) quiet() bool {
	if !r.wrote && !r.j.OpenWhenQuiet {
		r.resetKeep()
		return true
	}
	if !r.keepalive() {
		return false
	}
	r.resetKeep()
	return true
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
	status := Status(cause)
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

// Status is the status a broken stream is filed under: 504 when the upstream
// went quiet or never started, 502 for any other break.
func Status(cause error) int {
	if errors.Is(cause, ErrStall) || errors.Is(cause, ErrFirstChunk) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// ReasonText is the fixed message a client sees for each way a stream breaks.
func ReasonText(cause error) string {
	switch {
	case errors.Is(cause, ErrStall):
		return "The upstream stopped sending data"
	case errors.Is(cause, ErrFirstChunk):
		return "The upstream did not start answering in time"
	case errors.Is(cause, ErrTruncated):
		return "The upstream stream ended before it finished"
	case errors.Is(cause, ErrEventTooLarge):
		return "The upstream sent an event that is too large"
	case errors.Is(cause, ErrCollectedTooLarge):
		return "The upstream answer is too large"
	}
	return "The upstream stream ended with an error"
}

// maxUpstreamReason bounds the upstream's own words a client is shown, the same
// length the stored last-error gets.
const maxUpstreamReason = 300

// upstreamReason is the fixed sentence plus whatever the upstream said, because
// a client that is told "an error" cannot tell a capacity blip from a bad
// request. The text is stripped of markup and cut short before it leaves.
func upstreamReason(e *translate.ChunkError) string {
	detail := e.Type
	if detail != "" && e.Message != "" {
		detail += ": "
	}
	detail += e.Message
	if d := text.StripMarkup(detail, maxUpstreamReason); d != "" {
		return ReasonText(ErrUpstream) + ": " + d
	}
	return ReasonText(ErrUpstream)
}

// cut reports a stream that stopped without a finish, which ended the answer
// early rather than completing it. content is what the stream carried, so an
// empty answer stays the empty answer it always was.
func (r *runner) cut(content int) bool {
	return !r.ended && r.finish == "" && content > 0
}

// isDone reports the marker an OpenAI stream closes with. It finishes the
// answer without naming a reason, so a body that stops after it did finish.
func isDone(format string, data []byte) bool {
	return format == translate.FormatOpenAI && string(bytes.TrimSpace(data)) == "[DONE]"
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
		ev, err := r.pull.next(ctx, usageDeadline, r.keepC())
		if errors.Is(err, errKeepalive) {
			if !r.quiet() {
				return
			}
			continue
		}
		r.resetKeep()
		if errors.Is(err, errUsageWait) {
			r.send(writer.Close())
			return
		}
		if errors.Is(err, io.EOF) {
			if r.cut(r.streamed) {
				r.fail(ErrTruncated, ReasonText(ErrTruncated))
				return
			}
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
		if isDone(r.j.UpstreamFormat, ev.Data) {
			r.ended = true
			continue
		}
		r.track.Feed(ev.Data)
		for _, ch := range reader.Read(ev.Name, ev.Data) {
			if ch.Err != nil {
				r.fail(ErrUpstream, upstreamReason(ch.Err))
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
