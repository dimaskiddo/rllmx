package pipeline

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/executor"
	"rllmx/app/file"
	"rllmx/app/stream"
	"rllmx/app/translate"
	"rllmx/app/upstream"
)

// errNotOpen is what an error frame gets before the client's stream exists, so
// the pump does not count it as written.
var errNotOpen = errors.New("pipeline: the client stream is not open")

// maxAnswer bounds a buffered upstream answer.
const maxAnswer = 64 << 20

// respond turns a 2xx upstream answer into the client's answer. An answer that
// is not what was asked for (a page, an empty body, a broken stream before any
// byte) is the upstream's fault but not a verdict on the account, so it is
// answered 502 and nothing is locked or cleared.
func (c *call) respond(ctx context.Context, resp executor.Response) (Output, *accountFail) {
	kind := stream.KindOf(resp.Header.Get("Content-Type"))
	if c.compactJSON(kind) {
		return c.forwardCompact(ctx, resp)
	}
	if c.upStream && (kind == stream.KindSSE || kind == stream.KindNDJSON || kind == stream.KindEvents) {
		var src stream.Source = stream.NewReader(resp.Body)
		switch kind {
		case stream.KindNDJSON:
			src = stream.NewLines(resp.Body)
		case stream.KindEvents:
			src = stream.NewFrames(resp.Body, c.framesWire())
		}
		if c.clientStream {
			return c.relay(ctx, resp, src)
		}
		return c.collect(ctx, resp, src)
	}
	if c.clientStream || kind == stream.KindOther {
		return c.unexpected(resp, kind), nil
	}
	return c.buffered(ctx, resp)
}

// framesWire is what a binary-framed answer carries. The executor knows, not
// the format: a Kiro request and a Claude request to another provider both go
// out in "claude", and only one of them frames its answer.
func (c *call) framesWire() stream.FramesWire {
	switch c.dest.entry.Executor {
	case upstream.ExecutorBedrock:
		return stream.FramesBedrockClaude
	case upstream.ExecutorBedrockXAI:
		return stream.FramesBedrockOpenAI
	}
	return stream.FramesKiro
}

// unexpected answers an upstream that sent a page, or JSON where a stream was
// asked for.
func (c *call) unexpected(resp executor.Response, kind stream.Kind) Output {
	defer resp.Body.Close()
	msg := "The upstream did not send a stream"
	if kind == stream.KindOther {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, executor.MaxErrorBody))
		msg = "The upstream returned an unexpected page"
		if title := stream.PageTitle(raw); title != "" {
			msg += ": " + title
		}
	}
	return c.fail(http.StatusBadGateway, "upstream_5xx", msg)
}

func (c *call) accept(ctx context.Context) {
	if c.p.d.Quota != nil && c.dest.provider == antigravityID {
		c.p.d.Quota.Succeeded(c.row.ConnectionID, c.dest.model)
	}
	if err := c.p.d.Reporter.Succeed(ctx, c.row.ConnectionID, c.dest.model, c.attemptStart); err != nil && c.p.d.Log != nil {
		c.p.d.Log.Warningf("pipeline: clearing locks failed: %v", err)
	}
}

func (c *call) buffered(ctx context.Context, resp executor.Response) (Output, *accountFail) {
	raw, err := file.ReadCapped(resp.Body, c.p.d.MaxAnswer)
	_ = resp.Body.Close()
	if err != nil && !errors.Is(err, file.ErrTooLarge) {
		return c.readFailed(ctx)
	}
	if len(raw) == 0 || err != nil {
		return c.fail(http.StatusBadGateway, "upstream_5xx", "The upstream sent no usable answer"), nil
	}
	up, ok := translate.Lookup(c.dest.format)
	if !ok {
		return c.internal(ctx, errors.New("no codec for "+c.dest.format)), nil
	}
	if c.dest.entry.Transport.Quirks.ClineEnvelope {
		raw = unwrapEnvelope(raw)
	}
	ans, err := up.ReadResponse(raw, c.tctx)
	if err != nil || !hasContent(ans) {
		return c.fail(http.StatusBadGateway, "upstream_5xx", "The upstream sent no usable answer"), nil
	}
	if ans.Usage.Prompt == 0 && ans.Usage.Completion == 0 {
		ans.Usage = stream.Estimate(len(c.body), len(ans.Message.Text())+len(ans.Message.Reasoning))
	}
	return c.deliver(ctx, ans)
}

func (c *call) collect(ctx context.Context, resp executor.Response, src stream.Source) (Output, *accountFail) {
	got, err := stream.Collect(ctx, resp.Body, src, c.dest.format, c.tctx, c.p.d.Timers, len(c.body))
	if err != nil {
		if ctx.Err() != nil {
			return c.readFailed(ctx)
		}
		return Output{}, streamFail(err)
	}
	ans := got.Response
	if !hasContent(&ans) {
		return c.fail(http.StatusBadGateway, "upstream_5xx", "The upstream sent no usable answer"), nil
	}
	return c.deliver(ctx, &ans)
}

// deliver writes an accepted answer in the client's format and clears the
// account's locks.
func (c *call) deliver(ctx context.Context, ans *translate.Response) (Output, *accountFail) {
	if ans.Model == "" {
		ans.Model = c.dest.model
	}
	down, _ := translate.Lookup(c.format)
	body, err := down.WriteResponse(ans, c.tctx)
	if err != nil {
		return c.internal(ctx, err), nil
	}
	c.setUsage(ans.Usage)
	c.accept(ctx)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	out := stream.AddClientBuffer(c.format, body)
	if c.cacheKey != "" {
		c.remember(ans, out)
		h.Set(CacheHeader, cacheMiss)
	}
	return Output{Status: http.StatusOK, Header: h, Body: out}, nil
}

func (c *call) readFailed(ctx context.Context) (Output, *accountFail) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Output{}, &accountFail{status: http.StatusGatewayTimeout, class: "timeout", text: "The upstream did not answer in time"}
	}
	return c.closed(), nil
}

func hasContent(r *translate.Response) bool {
	m := r.Message
	return len(m.Content) > 0 || len(m.ToolCalls) > 0 || m.Reasoning != ""
}

func streamFail(err error) *accountFail {
	status := http.StatusBadGateway
	if errors.Is(err, stream.ErrStall) || errors.Is(err, stream.ErrFirstChunk) {
		status = http.StatusGatewayTimeout
	}
	return &accountFail{status: status, class: streamClass(err), text: stream.ReasonText(err)}
}

// relay streams the answer to the client. The client's stream is opened by the
// first frame, so an upstream that fails before sending anything can still be
// answered by the next account.
func (c *call) relay(ctx context.Context, resp executor.Response, src stream.Source) (Output, *accountFail) {
	if c.open == nil {
		_ = resp.Body.Close()
		return c.internal(ctx, errors.New("no way to open a stream")), nil
	}
	lc := &lazyClient{open: c.open}
	res := stream.Run(ctx, stream.Job{
		Body: resp.Body, Source: src, UpstreamFormat: c.dest.format, ClientFormat: c.format, Ctx: c.tctx,
		Client: lc, Timers: c.p.d.Timers, RequestBytes: len(c.body), Buffer: true, Now: c.p.d.Now,
		Rewrite: c.restoreEvent(),
	})
	if errors.Is(res.Err, stream.ErrClientGone) {
		c.setUsage(res.Usage)
		return c.closed(), nil
	}
	if !lc.opened {
		if res.Err != nil {
			return Output{}, streamFail(res.Err)
		}
		return c.fail(http.StatusBadGateway, "upstream_5xx", "The upstream sent no usable answer"), nil
	}
	c.setUsage(res.Usage)
	c.row.TTFBMs = res.TTFT.Milliseconds()
	if res.Err != nil {
		c.errClass = streamClass(res.Err)
		c.rowStatus = res.Status
		return Output{Status: http.StatusOK, Streamed: true}, nil
	}
	if res.TTFT > 0 {
		c.accept(ctx)
	}
	return Output{Status: http.StatusOK, Streamed: true}, nil
}

// lazyClient opens the client's stream on the first frame. An error frame the
// pump offers before that is dropped: the pipeline turns the failure into a
// plain error response or a retry.
type lazyClient struct {
	open   Opener
	client stream.Client
	opened bool
	err    error
}

func (l *lazyClient) start() error {
	if !l.opened {
		l.opened = true
		l.client, l.err = l.open()
	}
	return l.err
}

func (l *lazyClient) Frame(f translate.Frame) error {
	if err := l.start(); err != nil {
		return err
	}
	return l.client.Frame(f)
}

func (l *lazyClient) Raw(b []byte) error {
	if !l.opened || l.err != nil {
		return errNotOpen
	}
	return l.client.Raw(b)
}

// restoreEvent undoes the disguise on a passthrough stream: the one event that
// names a tool, the start of a tool_use block, gets the client's name back. It
// is nil when the attempt renamed nothing, so an undisguised stream is forwarded
// byte for byte.
func (c *call) restoreEvent() func(event string, data []byte) []byte {
	if c.tctx.ToolName == nil || c.dest.format != translate.FormatClaude {
		return nil
	}
	return func(_ string, data []byte) []byte {
		if gjson.GetBytes(data, "type").String() != "content_block_start" || gjson.GetBytes(data, "content_block.type").String() != "tool_use" {
			return data
		}
		name := gjson.GetBytes(data, "content_block.name").String()
		back := c.tctx.RestoreTool(name)
		if back == name {
			return data
		}
		out, err := sjson.SetBytes(data, "content_block.name", back)
		if err != nil {
			return data
		}
		return out
	}
}

// compactJSON is true for a context-compaction call to Codex that was answered
// with JSON. The call is sent as a stream, but the compaction route may answer
// with the compacted conversation in one piece.
func (c *call) compactJSON(kind stream.Kind) bool {
	return kind == stream.KindJSON && c.dest.entry.Executor == upstream.ExecutorCodex &&
		c.format == translate.FormatResponses && strings.HasSuffix(c.in.Path, "/compact")
}

// forwardCompact passes a compaction answer to the client as it came. Rebuilding
// it through the hub would drop the compaction items, which only the upstream
// knows how to read back. The usage is read for the row; the body is not kept.
func (c *call) forwardCompact(ctx context.Context, resp executor.Response) (Output, *accountFail) {
	raw, err := file.ReadCapped(resp.Body, c.p.d.MaxAnswer)
	_ = resp.Body.Close()
	if err != nil && !errors.Is(err, file.ErrTooLarge) {
		return c.readFailed(ctx)
	}
	if len(raw) == 0 || err != nil || !gjson.ValidBytes(raw) {
		return c.fail(http.StatusBadGateway, "upstream_5xx", "The upstream sent no usable answer"), nil
	}
	up, _ := translate.Lookup(c.dest.format)
	usage := stream.Estimate(len(c.body), len(raw))
	if ans, err := up.ReadResponse(raw, c.tctx); err == nil && (ans.Usage.Prompt > 0 || ans.Usage.Completion > 0) {
		usage = ans.Usage
	}
	c.setUsage(usage)
	c.accept(ctx)
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	return Output{Status: http.StatusOK, Header: h, Body: raw}, nil
}

// unwrapEnvelope returns the data of a {"success":true,"data":{...}} body, and
// any other body as it came. An error envelope never matches.
func unwrapEnvelope(raw []byte) []byte {
	if !gjson.ValidBytes(raw) {
		return raw
	}
	if ok := gjson.GetBytes(raw, "success"); ok.Type != gjson.True {
		return raw
	}
	if data := gjson.GetBytes(raw, "data"); data.IsObject() {
		return []byte(data.Raw)
	}
	return raw
}
