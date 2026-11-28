package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"rllmx/app/platform/file"
	"rllmx/app/platform/text"
)

const (
	// finishContinuation is a hop that stopped because the call's own output
	// limit was reached, not because the model finished.
	finishContinuation = "CONTINUATION"
	finishMaxTokens    = "MAX_TOKENS"
	// MaxHops bounds how many calls one answer may take: the ceiling divided by
	// what one call returns is 16, and the rest guards against a runaway upstream.
	MaxHops = 20
	// maxJoinedBytes bounds a joined JSON answer, so the hops of one answer
	// cannot grow without limit in memory.
	maxJoinedBytes = 16 << 20
	// hopOutput is what one call returns at most.
	hopOutput = 65536
)

// WantsLongDecode reports a request that asks for more than one call returns, on
// a model that continues.
func WantsLongDecode(req Request) bool {
	if !req.LongDecode || !gjson.ValidBytes(req.Body) {
		return false
	}
	for _, path := range []string{"generationConfig.maxOutputTokens", "request.generationConfig.maxOutputTokens"} {
		if n := gjson.GetBytes(req.Body, path); n.Exists() && n.Int() > hopOutput {
			return true
		}
	}
	return false
}

// Continue sends a request through next and, when the model continues, sends the
// same request again with the latest token until it finishes, handing back one
// answer: the hops' parts joined in one turn, their usage added. Anything that
// does not need it comes back as the first call answered it.
func Continue(ctx context.Context, next Executor, req Request, hops int) (Response, error) {
	resp, err := next.Execute(ctx, req)
	if err != nil || resp.Status < 200 || resp.Status > 299 || !WantsLongDecode(req) {
		return resp, err
	}
	run := &decoder{ctx: ctx, next: next, req: req, left: hops - 1}
	if req.Stream {
		return run.stream(resp), nil
	}
	return run.join(resp)
}

// decoder is one answer's hops.
type decoder struct {
	ctx  context.Context
	next Executor
	req  Request
	left int
}

// tokenPath is where a request carries the token: the envelope Cloud Code wraps
// a request in takes it inside, the Gemini API takes it at the top.
func (d *decoder) tokenPath() string {
	if gjson.GetBytes(d.req.Body, "request").IsObject() {
		return "request.continuationToken"
	}
	return "continuationToken"
}

// again sends the identical request carrying only the latest token. A failure
// of any kind is a hop that did not happen, not an error: the answer so far is
// still the answer.
func (d *decoder) again(token string) (Response, bool) {
	if d.left <= 0 || d.ctx.Err() != nil {
		return Response{}, false
	}
	d.left--
	body, err := sjson.SetBytes(d.req.Body, d.tokenPath(), token)
	if err != nil {
		return Response{}, false
	}
	req := d.req
	req.Body = body
	resp, err := d.next.Execute(d.ctx, req)
	if err != nil {
		return Response{}, false
	}
	if resp.Status < 200 || resp.Status > 299 {
		Drain(resp.Body)
		return Response{}, false
	}
	return resp, true
}

// unwrap is the Gemini answer inside the envelope Cloud Code puts around it, and
// whether there was one.
func unwrap(g gjson.Result) (gjson.Result, bool) {
	if !g.Get("candidates").Exists() {
		if inner := g.Get("response"); inner.IsObject() {
			return inner, true
		}
	}
	return g, false
}

// token is the continuation a candidate asks for, or "".
func token(g gjson.Result) string {
	c := g.Get("candidates.0")
	if c.Get("finishReason").String() != finishContinuation {
		return ""
	}
	return c.Get("continuationToken").String()
}

// join reads every hop and writes one JSON answer.
func (d *decoder) join(first Response) (Response, error) {
	raw, err := file.ReadCapped(first.Body, maxJoinedBytes)
	_ = first.Body.Close()
	if err != nil {
		return Response{}, errors.New("executor: the first answer of a long decode is unreadable")
	}
	root := gjson.ParseBytes(raw)
	if !root.IsObject() {
		return ReplyWith(first, first.Status, raw, ""), nil
	}
	inner, wrapped := unwrap(root)
	parts := []string{}
	for _, p := range inner.Get("candidates.0.content.parts").Array() {
		parts = append(parts, p.Raw)
	}
	usage := addUsage(nil, inner.Get("usageMetadata"))
	last, size := raw, len(raw)
	tok := token(inner)
	truncated := false
	for tok != "" {
		resp, ok := d.again(tok)
		if !ok {
			truncated = true
			break
		}
		body, rerr := file.ReadCapped(resp.Body, maxJoinedBytes)
		_ = resp.Body.Close()
		if size += len(body); rerr != nil || size > maxJoinedBytes || !gjson.ValidBytes(body) {
			truncated = true
			break
		}
		next, _ := unwrap(gjson.ParseBytes(body))
		for _, p := range next.Get("candidates.0.content.parts").Array() {
			parts = append(parts, p.Raw)
		}
		usage = addUsage(usage, next.Get("usageMetadata"))
		last, tok = body, token(next)
	}
	if tok != "" {
		truncated = true
	}
	out, err := compose(first, root, last, parts, usage, wrapped, truncated)
	if err != nil {
		return Response{}, err
	}
	resp := ReplyWith(first, first.Status, out, "")
	resp.Header.Del("Content-Length")
	return resp, nil
}

// compose writes the joined answer on the last hop's own shape, so the fields a
// provider adds beside the candidates survive.
func compose(first Response, root gjson.Result, last []byte, parts []string, usage map[string]float64, wrapped, truncated bool) ([]byte, error) {
	lastRoot := gjson.ParseBytes(last)
	if !lastRoot.IsObject() {
		lastRoot = root
	}
	lastInner, _ := unwrap(lastRoot)
	out := []byte(lastInner.Raw)
	if len(out) == 0 || !gjson.ValidBytes(out) {
		out = []byte(`{}`)
	}
	arr := "[" + strings.Join(parts, ",") + "]"
	out, _ = sjson.SetRawBytes(out, "candidates.0.content.parts", []byte(arr))
	if role := root.Get("candidates.0.content.role").String(); role != "" || gjson.Get(lastInner.Raw, "candidates.0.content.role").Exists() {
		if role == "" {
			role = "model"
		}
		out, _ = sjson.SetBytes(out, "candidates.0.content.role", role)
	}
	out, _ = sjson.DeleteBytes(out, "candidates.0.continuationToken")
	if truncated || gjson.GetBytes(out, "candidates.0.finishReason").String() == finishContinuation {
		out, _ = sjson.SetBytes(out, "candidates.0.finishReason", finishMaxTokens)
	}
	if len(usage) > 0 {
		out, _ = sjson.SetRawBytes(out, "usageMetadata", usageJSON(usage, lastInner.Get("usageMetadata")))
	}
	if !wrapped {
		return out, nil
	}
	env := []byte(lastRoot.Raw)
	if !gjson.ValidBytes(env) {
		env = []byte(root.Raw)
	}
	return sjson.SetRawBytes(env, "response", out)
}

// addUsage adds the numeric fields of one hop's usage to the running total.
func addUsage(total map[string]float64, u gjson.Result) map[string]float64 {
	if !u.IsObject() {
		return total
	}
	if total == nil {
		total = map[string]float64{}
	}
	u.ForEach(func(k, v gjson.Result) bool {
		if v.Type == gjson.Number {
			total[k.String()] += v.Float()
		}
		return true
	})
	return total
}

// usageJSON is the total on the last hop's own usage object, so a field that is
// not a count keeps its latest value.
func usageJSON(total map[string]float64, latest gjson.Result) []byte {
	out := []byte(`{}`)
	if latest.IsObject() {
		out = []byte(latest.Raw)
	}
	for k, v := range total {
		out, _ = sjson.SetBytes(out, text.EscapePath(k), int64(v))
	}
	return out
}

// stream is the first hop and the ones after it as one event stream.
func (d *decoder) stream(first Response) Response {
	s := &hopStream{d: d, live: first.Body, lines: bufio.NewReaderSize(first.Body, 32<<10)}
	resp := first
	resp.Body = s
	resp.Header = StreamHeader(first.Header)
	resp.Header.Del("Content-Length")
	resp.Header.Del("Content-Encoding")
	return resp
}

// hopStream writes the hops' events one after another. It hides a hop's own
// ending, offsets each hop's usage by the hops before it, and ends the turn as
// truncated when a hop cannot be had.
type hopStream struct {
	d     *decoder
	live  io.Closer
	lines *bufio.Reader
	out   bytes.Buffer
	// token is the continuation the hop being read asked for, and base is the
	// usage of the hops already finished; hop is the latest usage of this one.
	token   string
	base    map[string]float64
	hop     map[string]float64
	wrapped bool
	done    bool

	// mu guards live and closed: the stream's reader runs on its own goroutine
	// while the caller closes from another.
	mu     sync.Mutex
	closed bool
}

func (s *hopStream) Read(p []byte) (int, error) {
	for s.out.Len() == 0 {
		if s.done || s.isClosed() {
			return 0, io.EOF
		}
		s.advance()
	}
	return s.out.Read(p)
}

func (s *hopStream) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *hopStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return s.live.Close()
}

// swap makes body the live hop, or closes it when the stream was closed while
// it was being opened.
func (s *hopStream) swap(body io.ReadCloser) bool {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		// The drain is a read, so it runs outside the lock: Close and isClosed
		// must not wait on a peer that has stopped sending.
		Drain(body)
		return false
	}
	s.live = body
	s.mu.Unlock()
	return true
}

func (s *hopStream) advance() {
	line, err := readHopLine(s.lines)
	if len(line) > 0 {
		s.out.Write(s.transform(bytes.TrimRight(line, "\r\n")))
		s.out.WriteByte('\n')
	}
	if err == nil {
		return
	}
	_ = s.live.Close()
	if errors.Is(err, errHopLineTooLong) {
		s.truncate()
		return
	}
	if s.token == "" {
		s.done = true
		return
	}
	resp, ok := s.d.again(s.token)
	if !ok || !isEvents(resp) {
		if ok {
			Drain(resp.Body)
		}
		s.truncate()
		return
	}
	if !s.swap(resp.Body) {
		s.done = true
		return
	}
	s.base = sum(s.base, s.hop)
	s.hop, s.token = nil, ""
	s.lines = bufio.NewReaderSize(resp.Body, 32<<10)
}

// maxHopLine bounds one line of a hop's stream; an upstream that sends none
// of the line ends would otherwise grow the buffer without limit.
const maxHopLine = 4 << 20

var errHopLineTooLong = errors.New("executor: a hop's line is too long")

// readHopLine reads one line, ends included, and refuses one past maxHopLine.
func readHopLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxHopLine {
			return nil, errHopLineTooLong
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func isEvents(r Response) bool {
	ct := r.Header.Get("Content-Type")
	return ct == "" || len(ct) >= 17 && ct[:17] == "text/event-stream"
}

// truncate ends the turn the way a limit does, with the usage so far.
func (s *hopStream) truncate() {
	resp := []byte(`{"candidates":[{"finishReason":"` + finishMaxTokens + `","index":0}]}`)
	if u := sum(s.base, s.hop); len(u) > 0 {
		resp, _ = sjson.SetRawBytes(resp, "usageMetadata", usageJSON(u, gjson.Result{}))
	}
	if s.wrapped {
		resp, _ = sjson.SetRawBytes([]byte(`{}`), "response", resp)
	}
	s.out.WriteString("data: ")
	s.out.Write(resp)
	s.out.WriteString("\n\n")
	s.done = true
}

// transform rewrites one data line; anything else passes as it came.
func (s *hopStream) transform(line []byte) []byte {
	data, ok := bytes.CutPrefix(line, []byte("data:"))
	if !ok || !gjson.ValidBytes(bytes.TrimSpace(data)) {
		return line
	}
	root := gjson.ParseBytes(bytes.TrimSpace(data))
	if !root.IsObject() {
		return line
	}
	inner, wrapped := unwrap(root)
	if wrapped {
		s.wrapped = true
	}
	obj := []byte(inner.Raw)
	switch tok := token(inner); {
	case tok != "":
		s.token = tok
		obj, _ = sjson.DeleteBytes(obj, "candidates.0.finishReason")
	case inner.Get("candidates.0.finishReason").String() == finishContinuation:
		obj, _ = sjson.SetBytes(obj, "candidates.0.finishReason", finishMaxTokens)
	}
	obj, _ = sjson.DeleteBytes(obj, "candidates.0.continuationToken")
	obj, _ = sjson.DeleteBytes(obj, "continuationToken")
	if u := inner.Get("usageMetadata"); u.IsObject() {
		s.hop = addUsage(nil, u)
		obj, _ = sjson.SetRawBytes(obj, "usageMetadata", usageJSON(sum(s.base, s.hop), u))
	}
	if wrapped {
		env, _ := sjson.SetRawBytes([]byte(root.Raw), "response", obj)
		obj = env
	}
	return append([]byte("data: "), obj...)
}

func sum(a, b map[string]float64) map[string]float64 {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]float64, len(a)+len(b))
	for k, v := range a {
		out[k] += v
	}
	for k, v := range b {
		out[k] += v
	}
	return out
}
