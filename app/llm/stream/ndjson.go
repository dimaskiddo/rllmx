package stream

import (
	"errors"
	"net/http"

	"rllmx/app/llm/translate"
)

// ErrLineBreak means a line passed to an NDJSON writer holds a line break.
var ErrLineBreak = errors.New("stream: a line contains a line break")

// NDJSON writes newline-delimited JSON, the stream format of Ollama clients.
// It follows the same rules as SSE: flush after every line, a write deadline,
// and a sticky error once the client is gone.
type NDJSON struct{ *sink }

// NewNDJSON starts a newline-delimited JSON stream on w. A handler passes
// WriterOf(ctx).
func NewNDJSON(w http.ResponseWriter, opt Options) (*NDJSON, error) {
	k, err := newSink(w, opt, "application/x-ndjson")
	if err != nil {
		return nil, err
	}
	k.w.Header().Set("Cache-Control", "no-cache, no-transform")
	return &NDJSON{k}, k.start()
}

func hasLineBreak[T string | []byte](data T) bool {
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' || data[i] == '\r' {
			return true
		}
	}
	return false
}

func line[T string | []byte](s *sink, data T) error {
	if hasLineBreak(data) {
		return ErrLineBreak
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := append(s.buf[:0], data...)
	b = append(b, '\n')
	err := s.writeLocked(b)
	s.keep(b)
	return err
}

// Line writes one JSON value as a line. A value with a line break would split
// into two lines and is refused.
func (n *NDJSON) Line(data string) error { return line(n.sink, data) }

// Raw writes a pre-built frame, such as an error line from the failure package.
func (n *NDJSON) Raw(frame []byte) error { return n.write(frame) }

// Frame writes one frame from a stream writer. A format that streams lines has
// no event names and no end marker.
func (n *NDJSON) Frame(f translate.Frame) error {
	if f.Done {
		return nil
	}
	return line(n.sink, f.Data)
}
