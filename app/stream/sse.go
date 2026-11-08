// Package stream holds what every streaming answer needs. The SSE writer is
// the only sanctioned way to write a server-sent-event response.
package stream

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/log"

	"rllmx/app/translate"
)

// DefaultWriteTimeout bounds one write to a client that has stopped reading.
const DefaultWriteTimeout = 60 * time.Second

// SSE writer errors.
var (
	ErrInvalidEventName = errors.New("stream: event name contains a line break")
	ErrNoFlush          = errors.New("stream: response writer cannot flush")
)

// Options tunes an SSE writer.
type Options struct {
	// WriteTimeout is the deadline set before each write; zero sets none.
	WriteTimeout time.Duration
	// Log receives a notice when the writer cannot honor WriteTimeout; nil is silent.
	Log log.Log
}

// sink is the half of a stream writer both wire formats share: it flushes
// after every frame, sets a write deadline and, once a write failed, fails
// every later call too, so a caller stops spending upstream tokens at the
// first error.
type sink struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	opt Options

	mu     sync.Mutex
	err    error
	warned bool
	// buf is the scratch space one frame is built in, reused for the next.
	buf []byte
}

// SSE writes server-sent events. Every method returns an error when the client
// is gone, and once one failed all later calls fail too.
type SSE struct{ *sink }

// unwrapper is implemented by Gin's response writer and by the wrappers
// net/http documents for http.ResponseController.
type unwrapper interface{ Unwrap() http.ResponseWriter }

// headerNow is implemented by Gin's writer; it sends the status line at once.
type headerNow interface{ WriteHeaderNow() }

// flushError and the plain Flusher are the two forms http.ResponseController
// accepts; asking for one does not send anything.
type flushError interface{ FlushError() error }

// NewSSE starts an event stream on w. Frames are written through w, so wrappers
// keep counting bytes; flushing and deadlines use the innermost writer, because
// Gin's wrapper hides flush errors. A handler passes WriterOf(ctx).
func NewSSE(w http.ResponseWriter, opt Options) (*SSE, error) {
	k, err := newSink(w, opt, "text/event-stream")
	if err != nil {
		return nil, err
	}
	k.w.Header().Set("Cache-Control", "no-cache, no-transform")
	return &SSE{k}, k.start()
}

// newSink checks that w can flush and returns a sink with the headers set.
func newSink(w http.ResponseWriter, opt Options, contentType string) (*sink, error) {
	inner := w
	for {
		u, ok := inner.(unwrapper)
		if !ok {
			break
		}
		next := u.Unwrap()
		if next == nil {
			break
		}
		inner = next
	}
	if !canFlush(inner) {
		return nil, ErrNoFlush
	}
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("X-Accel-Buffering", "no")
	h.Del("Content-Length")
	return &sink{w: w, rc: http.NewResponseController(inner), opt: opt}, nil
}

// start sends the status line at once.
func (s *sink) start() error {
	s.w.WriteHeader(http.StatusOK)
	if hn, ok := s.w.(headerNow); ok {
		hn.WriteHeaderNow()
	}
	return nil
}

func canFlush(w http.ResponseWriter) bool {
	switch w.(type) {
	case flushError, http.Flusher:
		return true
	}
	return false
}

func (s *sink) fail(err error) error {
	if s.err == nil {
		s.err = err
	}
	return s.err
}

// maxScratch is the largest frame buffer a sink keeps between frames.
const maxScratch = 256 << 10

func (s *sink) write(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(frame)
}

// keep stores the scratch buffer for the next frame unless one frame made it
// large. Called under the lock.
func (s *sink) keep(b []byte) {
	if cap(b) > maxScratch {
		b = nil
	}
	s.buf = b
}

func (s *sink) writeLocked(frame []byte) error {
	if s.err != nil {
		return s.err
	}
	if s.opt.WriteTimeout > 0 {
		if err := s.rc.SetWriteDeadline(time.Now().Add(s.opt.WriteTimeout)); err != nil && !s.warned {
			s.warned = true
			if s.opt.Log != nil {
				s.opt.Log.Warningf("write deadline not supported, a stalled client is not cut off: %v", err)
			}
		}
	}
	if _, err := s.w.Write(frame); err != nil {
		return s.fail(err)
	}
	if err := s.rc.Flush(); err != nil {
		return s.fail(err)
	}
	return nil
}

// appendDataLines appends a payload as data fields, one per line. A bare CR, a
// CRLF and an LF all end a line, so a payload can never add a field of its own.
func appendDataLines[T string | []byte](b []byte, data T) []byte {
	start := 0
	for i := 0; i < len(data); i++ {
		c := data[i]
		if c != '\n' && c != '\r' {
			continue
		}
		b = append(b, "data: "...)
		b = append(b, data[start:i]...)
		b = append(b, '\n')
		if c == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			i++
		}
		start = i + 1
	}
	b = append(b, "data: "...)
	b = append(b, data[start:]...)
	return append(b, '\n')
}

// emit builds one event in the sink's scratch buffer and writes it.
func emit[T string | []byte](s *sink, named bool, name string, data T) error {
	if named && strings.ContainsAny(name, "\r\n") {
		return ErrInvalidEventName
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.buf[:0]
	if named {
		b = append(b, "event: "...)
		b = append(b, name...)
		b = append(b, '\n')
	}
	b = appendDataLines(b, data)
	b = append(b, '\n')
	err := s.writeLocked(b)
	s.keep(b)
	return err
}

// Data writes an unnamed event.
func (s *SSE) Data(data string) error { return emit(s.sink, false, "", data) }

// Event writes a named event.
func (s *SSE) Event(name, data string) error { return emit(s.sink, true, name, data) }

// Raw writes a pre-built frame, such as an error frame from the failure package.
func (s *SSE) Raw(frame []byte) error { return s.write(frame) }

// Frame writes one frame from a stream writer: a named event, a plain data
// event, or the [DONE] marker.
func (s *SSE) Frame(f translate.Frame) error {
	switch {
	case f.Done:
		return emit(s.sink, false, "", "[DONE]")
	case f.Event != "":
		return emit(s.sink, true, f.Event, f.Data)
	}
	return emit(s.sink, false, "", f.Data)
}
