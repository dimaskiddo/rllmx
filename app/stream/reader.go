package stream

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// MaxEventBytes caps one event or line from an upstream.
const MaxEventBytes = 1 << 20

const bom = "\ufeff"

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
}

// NewReader wraps an upstream body.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 32<<10), first: true}
}

// readLine returns one line without its end, treating CR, LF and CRLF alike.
func readLine(r *bufio.Reader, limit int) (string, error) {
	var b []byte
	for {
		c, err := r.ReadByte()
		if err != nil {
			if err == io.EOF && len(b) > 0 {
				return string(b), nil
			}
			return "", err
		}
		switch c {
		case '\n':
			return string(b), nil
		case '\r':
			if next, err := r.Peek(1); err == nil && next[0] == '\n' {
				_, _ = r.ReadByte()
			}
			return string(b), nil
		}
		if len(b) >= limit {
			return "", ErrEventTooLarge
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
	var data []string
	size := 0
	flush := func() (Event, bool) {
		if len(data) == 0 {
			name = ""
			return Event{}, false
		}
		ev := Event{Name: name, Data: []byte(strings.Join(data, "\n"))}
		return ev, true
	}
	for {
		line, err := readLine(s.r, MaxEventBytes)
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
			line = strings.TrimPrefix(line, bom)
		}
		if line == "" {
			if ev, ok := flush(); ok {
				return ev, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			name = value
		case "data":
			size += len(value) + 1
			if size > MaxEventBytes {
				s.err = ErrEventTooLarge
				return Event{}, s.err
			}
			data = append(data, value)
		}
	}
}

// Lines reads newline-delimited JSON. A blank line is skipped, and a line that
// does not start a JSON object is not an event.
type Lines struct {
	r   *bufio.Reader
	err error
}

// NewLines wraps an upstream body.
func NewLines(r io.Reader) *Lines { return &Lines{r: bufio.NewReaderSize(r, 32<<10)} }

// Next returns the next JSON line, or io.EOF.
func (l *Lines) Next() (Event, error) {
	if l.err != nil {
		return Event{}, l.err
	}
	for {
		line, err := readLine(l.r, MaxEventBytes)
		if err != nil {
			l.err = err
			return Event{}, err
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, bom))
		if len(line) > 0 && line[0] == '{' {
			return Event{Data: []byte(line)}, nil
		}
	}
}

// Source is what the pump reads events from.
type Source interface {
	Next() (Event, error)
}
