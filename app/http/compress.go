package http

import (
	"bytes"
	"compress/gzip"
	"io"
	nethttp "net/http"
	"strconv"
	"strings"
)

// compressThreshold is the smallest answer worth compressing. Below it the
// bytes are sent as the handler wrote them, with their own Content-Length.
const compressThreshold = 1024

// gzipLevel favours speed: a non-stream answer can carry 64 MiB of base64 image
// or audio, where a high level spends CPU for a small gain on dense data.
const gzipLevel = gzip.BestSpeed

// encoder builds one response encoder.
type encoder struct {
	name string
	wrap func(io.Writer) io.WriteCloser
}

// encoders are offered in this order, and the first the client accepts wins.
// Brotli joins ahead of gzip once its module is approved (NOTES, dependency
// queue): it is not in the standard library.
var encoders = []encoder{
	{name: "gzip", wrap: func(w io.Writer) io.WriteCloser {
		gw, _ := gzip.NewWriterLevel(w, gzipLevel)
		return gw
	}},
}

// compressibleTypes are the media types worth compressing. A type is read with
// its parameters dropped and compared case-insensitively; a "+json" or "+xml"
// suffix counts as its base type.
var compressibleTypes = []string{
	"application/json", "application/xml", "application/javascript",
	"text/", "image/svg+xml",
}

// secretPaths carry a credential or a token and are never compressed.
var secretPaths = []string{"/api/oauth/", "/api/auth/", "/api/providers/"}

// Compress wraps next so an answer worth it leaves compressed, as the client's
// Accept-Encoding allows. It sits outside the router, below Gin's writer, so it
// sees both write paths: a handler's ctx.Response() comes down through Gin's
// wrapper to this writer, and a stream's stream.WriterOf(ctx) already steps
// past Gin's wrapper to the same one. A stream is excluded by its content type
// and by a Flush that chooses pass-through at the first frame.
func Compress(enabled bool, next nethttp.Handler) nethttp.Handler {
	if !enabled {
		return next
	}
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.Method == nethttp.MethodHead || secretPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		cw := &compressWriter{ResponseWriter: w, enc: acceptedEncoder(r.Header.Get("Accept-Encoding"))}
		done := false
		defer func() {
			if !done {
				// The handler panicked. What was written is sent as it stands, and
				// nothing is ever closed: a valid trailer on a broken body would
				// hide the failure the re-panic is meant to show.
				cw.abandon()
			}
		}()
		next.ServeHTTP(cw, r)
		done = true
		cw.finish()
	})
}

// secretPath reports a path whose answer may carry a secret.
func secretPath(p string) bool {
	for _, prefix := range secretPaths {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// acceptedEncoder is the encoder to use for the client's Accept-Encoding, or
// the zero encoder when it accepts none of ours. Between two encoders the
// client weights the same, the earlier in the table wins.
func acceptedEncoder(raw string) string {
	if raw == "" {
		return ""
	}
	q := quality(raw)
	best, bestQ := "", 0.0
	for _, e := range encoders {
		if v := q(e.name); v > bestQ {
			best, bestQ = e.name, v
		}
	}
	return best
}

// quality reads the q-value of every coding in an Accept-Encoding value. An
// unnamed coding takes the weight of "*", else 0; a q of 0 refuses a coding
// outright, including one "*" would otherwise allow.
func quality(raw string) func(string) float64 {
	named := map[string]float64{}
	star, starSeen := 0.0, false
	for part := range strings.SplitSeq(raw, ",") {
		name, q := acceptPart(part)
		if name == "*" {
			star, starSeen = q, true
			continue
		}
		named[name] = q
	}
	return func(name string) float64 {
		if v, ok := named[name]; ok {
			return v
		}
		if starSeen {
			return star
		}
		return 0
	}
}

// acceptPart reads one "coding" or "coding;q=0.x" item, q defaulting to 1.
func acceptPart(part string) (name string, q float64) {
	name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
	name = strings.ToLower(strings.TrimSpace(name))
	q = 1
	for p := range strings.SplitSeq(params, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "q") {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || n < 0 || n > 1 {
			// A weight this router cannot read is not an acceptance: the coding
			// it names is refused rather than assumed.
			q = 0
			continue
		}
		q = n
	}
	return name, q
}

// compressible reports a Content-Type worth compressing, parameters and case
// ignored.
func compressible(contentType string) bool {
	ct, _, _ := strings.Cut(contentType, ";")
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false
	}
	for _, p := range compressibleTypes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return strings.HasSuffix(ct, "+json") || strings.HasSuffix(ct, "+xml")
}

// state is what compressWriter decided, once it has decided.
type state int

const (
	pending state = iota
	passThrough
	compressing
)

// compressWriter sits between the handler chain and the real connection. It
// implements Unwrap, Flush and FlushError so http.ResponseController (the
// Deadlines middleware) and Gin's own Flush still reach the socket.
type compressWriter struct {
	nethttp.ResponseWriter
	enc string

	st     state
	buf    bytes.Buffer
	body   io.WriteCloser
	code   int
	closed bool
}

// Unwrap lets http.ResponseController and Gin's own unwrap chain reach the
// connection below this writer.
func (c *compressWriter) Unwrap() nethttp.ResponseWriter { return c.ResponseWriter }

// Flush means the caller is streaming. The status and whatever is held go out
// as they are, with no length, and every later byte passes through: a frame
// cannot wait for a threshold that a live answer never reaches.
func (c *compressWriter) Flush() {
	if c.st == pending {
		if c.code == 0 {
			c.code = nethttp.StatusOK
			c.markVary()
		}
		c.st = passThrough
		c.ResponseWriter.WriteHeader(c.code)
		if c.buf.Len() > 0 {
			_, _ = c.ResponseWriter.Write(c.buf.Bytes())
			c.buf.Reset()
		}
	}
	if f, ok := c.ResponseWriter.(nethttp.Flusher); ok {
		f.Flush()
	}
}

// FlushError is the form http.ResponseController asks a writer for first.
func (c *compressWriter) FlushError() error {
	c.Flush()
	return nil
}

// WriteHeader records the status, and for a response that will never be
// encoded it sends it at once: a stream flushes through the writer below this
// one, which sends the status itself, so waiting until the first Write would
// both hold the frame back and send the status a second time. An informational
// status is sent at once and does not become the final one.
func (c *compressWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != nethttp.StatusSwitchingProtocols {
		c.ResponseWriter.WriteHeader(code)
		return
	}
	if c.code == 0 {
		c.code = code
		c.markVary()
		c.settle()
	}
}

// settle sends the status now when no byte of this response can be encoded.
// A compressible answer keeps waiting for the bytes that decide.
func (c *compressWriter) settle() {
	if c.enc != "" && c.eligible() {
		return
	}
	c.st = passThrough
	c.ResponseWriter.WriteHeader(c.code)
}

// markVary adds Accept-Encoding to Vary on a response a client could have got
// in two forms, compressed or not: a cache has to keep them apart, and a short
// answer is still one of the two. A response another layer already encoded, a
// transformed one and a type that is never compressed (a stream, media) do not
// vary.
func (c *compressWriter) markVary() {
	h := c.Header()
	switch {
	case c.enc == "", h.Get("Content-Encoding") != "", h.Get("Content-Range") != "":
		return
	case strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-transform"):
		return
	case !compressible(h.Get("Content-Type")):
		return
	}
	addVary(h)
}

// Write holds the first compressThreshold bytes, decides once, then either
// passes every byte on or encodes it. It always reports len(p): the handler
// wrote those bytes, whatever left the socket.
func (c *compressWriter) Write(p []byte) (int, error) {
	if c.code == 0 {
		c.code = nethttp.StatusOK
		c.markVary()
		c.settle()
	}
	switch c.st {
	case passThrough:
		return c.ResponseWriter.Write(p)
	case compressing:
		if _, err := c.body.Write(p); err != nil {
			return 0, err
		}
		return len(p), nil
	}
	c.buf.Write(p)
	if c.buf.Len() < compressThreshold {
		return len(p), nil
	}
	if err := c.decide(); err != nil {
		return 0, err
	}
	return len(p), nil
}

// decide makes the one choice for this response and sends what it holds.
func (c *compressWriter) decide() error {
	if c.enc != "" && c.worthCompressing() {
		h := c.Header()
		h.Set("Content-Encoding", c.enc)
		h.Del("Content-Length")
		weakenETag(h)
		c.st = compressing
		for _, e := range encoders {
			if e.name == c.enc {
				c.body = e.wrap(c.ResponseWriter)
				break
			}
		}
		c.ResponseWriter.WriteHeader(c.code)
		_, err := c.body.Write(c.buf.Bytes())
		c.buf.Reset()
		return err
	}
	c.send(c.buf.Bytes())
	return nil
}

// worthCompressing applies the status, encoding and type rules, adding Vary so
// a cache keeps the two forms of a compressible answer apart.
func (c *compressWriter) worthCompressing() bool {
	if !c.eligible() {
		return false
	}
	addVary(c.Header())
	return true
}

// eligible reports a response whose type and status a client could receive
// encoded: everything but a bodyless status, an answer another layer encoded
// or transformed, and a type never worth compressing.
func (c *compressWriter) eligible() bool {
	h := c.Header()
	switch {
	case c.code < 200, c.code == nethttp.StatusNoContent, c.code == nethttp.StatusNotModified:
		return false
	case h.Get("Content-Encoding") != "":
		return false
	case h.Get("Content-Range") != "":
		return false
	case strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-transform"):
		return false
	case !compressible(h.Get("Content-Type")):
		return false
	}
	return true
}

// send writes the body as the handler gave it, with its exact length. A status
// that carries no body by the specification never gets a length either.
func (c *compressWriter) send(body []byte) {
	c.st = passThrough
	if c.code != nethttp.StatusNoContent && c.code != nethttp.StatusNotModified {
		c.Header().Set("Content-Length", strconv.Itoa(len(body)))
	}
	c.ResponseWriter.WriteHeader(c.code)
	if len(body) > 0 {
		_, _ = c.ResponseWriter.Write(body)
	}
	c.buf.Reset()
}

// finish runs after the handler returned normally. A response that never
// crossed the threshold is decided here; an encoded one is closed so its trailer
// reaches the client.
func (c *compressWriter) finish() {
	if c.code == 0 {
		// Nothing was written: there is no body to decide about.
		return
	}
	if c.st == pending {
		if c.buf.Len() >= compressThreshold {
			_ = c.decide()
		} else {
			c.send(c.buf.Bytes())
		}
	}
	if c.st == compressing && c.body != nil && !c.closed {
		c.closed = true
		_ = c.body.Close()
	}
}

// abandon runs when the handler panicked. What was written is sent as it
// stands; an encoder that had started is left open, so the client never sees a
// valid trailer on a body that will never be complete.
func (c *compressWriter) abandon() {
	if c.st == pending && c.code != 0 {
		c.send(c.buf.Bytes())
	}
	if c.body != nil && !c.closed {
		c.closed = true
		// Reset to the discard stream: whatever the encoder still holds is not
		// written, and it is not closed on a panic.
		if r, ok := c.body.(interface{ Reset(io.Writer) }); ok {
			r.Reset(io.Discard)
		}
	}
}

// addVary adds Accept-Encoding to Vary without disturbing a value another layer
// already set.
func addVary(h nethttp.Header) {
	for _, v := range h.Values("Vary") {
		for f := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(f), "Accept-Encoding") {
				return
			}
		}
	}
	h.Add("Vary", "Accept-Encoding")
}

// weakenETag turns a strong validator into a weak one: the encoded and plain
// bytes differ, but they are the same resource.
func weakenETag(h nethttp.Header) {
	if v := h.Get("ETag"); v != "" && !strings.HasPrefix(v, "W/") {
		h.Set("ETag", "W/"+v)
	}
}
