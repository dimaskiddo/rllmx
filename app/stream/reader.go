package stream

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxEventBytes caps one event or line from an upstream.
const MaxEventBytes = 1 << 20

const bom = "\ufeff"

var (
	bomBytes = []byte(bom)
	colon    = []byte(":")
	space    = []byte(" ")
)

// ErrEventTooLarge means one upstream event passed MaxEventBytes.
var ErrEventTooLarge = errors.New("stream: an upstream event is too large")

// Event is one upstream event: SSE gives a name and the joined data lines,
// NDJSON gives the line as Data.
type Event struct {
	Name string
	Data []byte
}

// Reader reads server-sent events. It accepts LF, CR and CRLF line ends, drops
// a leading byte-order mark and ignores comments, ids and retry hints.
type Reader struct {
	r     *bufio.Reader
	first bool
	err   error
	line  []byte
}

// NewReader wraps an upstream body.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 32<<10), first: true}
}

// readLine reads one line into buf, without its end, treating CR, LF and CRLF
// alike. The result is only valid until the next call that reuses buf.
func readLine(r *bufio.Reader, buf []byte, limit int) ([]byte, error) {
	b := buf[:0]
	for {
		c, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && len(b) > 0 {
				return b, nil
			}
			return b, err
		}
		switch c {
		case '\n':
			return b, nil
		case '\r':
			if next, err := r.Peek(1); err == nil && next[0] == '\n' {
				_, _ = r.ReadByte()
			}
			return b, nil
		}
		if len(b) >= limit {
			return b, ErrEventTooLarge
		}
		b = append(b, c)
	}
}

// Next returns the next event, or io.EOF when the body ends. An event with no
// data lines is skipped.
func (s *Reader) Next() (Event, error) {
	if s.err != nil {
		return Event{}, s.err
	}
	var name string
	var data []byte
	lines := 0
	size := 0
	flush := func() (Event, bool) {
		if lines == 0 {
			name = ""
			return Event{}, false
		}
		return Event{Name: name, Data: data}, true
	}
	for {
		line, err := readLine(s.r, s.line, MaxEventBytes)
		s.line = line[:0]
		if err != nil {
			if err == io.EOF {
				s.err = io.EOF
				if ev, ok := flush(); ok {
					return ev, nil
				}
				return Event{}, io.EOF
			}
			s.err = err
			return Event{}, err
		}
		if s.first {
			s.first = false
			line = bytes.TrimPrefix(line, bomBytes)
		}
		if len(line) == 0 {
			if ev, ok := flush(); ok {
				return ev, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, colon)
		value = bytes.TrimPrefix(value, space)
		switch string(field) {
		case "event":
			name = string(value)
		case "data":
			size += len(value) + 1
			if size > MaxEventBytes {
				s.err = ErrEventTooLarge
				return Event{}, s.err
			}
			if lines == 0 {
				data = make([]byte, len(value), len(value)+len(value)/4)
				copy(data, value)
			} else {
				data = append(data, '\n')
				data = append(data, value...)
			}
			lines++
		}
	}
}

// Lines reads newline-delimited JSON. A blank line is skipped, and a line that
// does not start a JSON object is not an event.
type Lines struct {
	r    *bufio.Reader
	err  error
	line []byte
}

// NewLines wraps an upstream body.
func NewLines(r io.Reader) *Lines { return &Lines{r: bufio.NewReaderSize(r, 32<<10)} }

// Next returns the next JSON line, or io.EOF.
func (l *Lines) Next() (Event, error) {
	if l.err != nil {
		return Event{}, l.err
	}
	for {
		line, err := readLine(l.r, l.line, MaxEventBytes)
		l.line = line[:0]
		if err != nil {
			l.err = err
			return Event{}, err
		}
		line = bytes.TrimSpace(bytes.TrimPrefix(line, bomBytes))
		if len(line) > 0 && line[0] == '{' {
			return Event{Data: bytes.Clone(line)}, nil
		}
	}
}

// Source is what the pump reads events from.
type Source interface {
	Next() (Event, error)
}
