// Package eventstream reads and writes AWS EventStream framing, the binary
// format Bedrock and CodeWhisperer answer a streamed request with. It is a
// protocol package: it knows bytes, not providers.
package eventstream

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// Why a frame is refused. Every one aborts the frame; a truncated frame is
// never handed on as a shorter answer.
var (
	ErrShort     = errors.New("eventstream: frame is shorter than its prelude")
	ErrBounds    = errors.New("eventstream: frame bounds are invalid")
	ErrPrelude   = errors.New("eventstream: prelude CRC mismatch")
	ErrMessage   = errors.New("eventstream: message CRC mismatch")
	ErrHeader    = errors.New("eventstream: header is malformed")
	ErrDuplicate = errors.New("eventstream: duplicate header")
)

// Bounds caps one frame. Both values are the protocol's own ceilings, so a
// hostile or broken upstream cannot make the router buffer more than this.
type Bounds struct {
	MaxMessage int
	MaxHeaders int
}

// DefaultBounds is the pair every AWS EventStream consumer here uses.
var DefaultBounds = Bounds{MaxMessage: 24 << 20, MaxHeaders: 128 << 10}

// Header value type codes of the framing.
const (
	headerTrue      = 0
	headerFalse     = 1
	headerByte      = 2
	headerShort     = 3
	headerInt       = 4
	headerBytes     = 6
	headerString    = 7
	headerTimestamp = 5
	headerUUID      = 9
)

// prelude is the fixed part before the headers: total length, header length
// and their CRC.
const prelude = 12

// Frame is one decoded frame: its headers by name and its payload as it
// arrived. A payload of zero bytes is nil.
type Frame struct {
	Headers map[string]any
	Payload []byte
}

// Type is a header read as text, which is how every header this program reads
// is written.
func (f Frame) Type(name string) string {
	s, _ := f.Headers[name].(string)
	return s
}

// Reader decodes frames from one upstream body.
type Reader struct {
	r      io.Reader
	bounds Bounds
	buf    []byte
}

// NewReader decodes frames from r under the default bounds.
func NewReader(r io.Reader) *Reader { return &Reader{r: r, bounds: DefaultBounds} }

// Next returns the next frame, or io.EOF at a clean end. Any other error means
// the framing broke and the answer is incomplete.
func (r *Reader) Next() (Frame, error) {
	var pre [prelude]byte
	if _, err := io.ReadFull(r.r, pre[:]); err != nil {
		return Frame{}, err
	}
	total := int(binary.BigEndian.Uint32(pre[0:4]))
	headersLen := int(binary.BigEndian.Uint32(pre[4:8]))
	if total < prelude+4 || total > r.bounds.MaxMessage || headersLen > r.bounds.MaxHeaders || headersLen > total-prelude-4 {
		return Frame{}, fmt.Errorf("%w: total %d headers %d", ErrBounds, total, headersLen)
	}
	if crc32.ChecksumIEEE(pre[0:8]) != binary.BigEndian.Uint32(pre[8:12]) {
		return Frame{}, ErrPrelude
	}
	if cap(r.buf) < total {
		r.buf = make([]byte, total)
	}
	frame := r.buf[:total]
	copy(frame, pre[:])
	if _, err := io.ReadFull(r.r, frame[prelude:]); err != nil {
		return Frame{}, err
	}
	if crc32.ChecksumIEEE(frame[:total-4]) != binary.BigEndian.Uint32(frame[total-4:]) {
		return Frame{}, ErrMessage
	}
	headers, err := parseHeaders(frame[prelude : prelude+headersLen])
	if err != nil {
		return Frame{}, err
	}
	payload := frame[prelude+headersLen : total-4]
	if len(payload) == 0 {
		return Frame{Headers: headers}, nil
	}
	out := make([]byte, len(payload))
	copy(out, payload)
	return Frame{Headers: headers, Payload: out}, nil
}

// parseHeaders reads the header block: [nameLen u8][name][type u8][value].
func parseHeaders(b []byte) (map[string]any, error) {
	headers := make(map[string]any, 4)
	for len(b) > 0 {
		nameLen := int(b[0])
		if len(b) < 1+nameLen+1 {
			return nil, ErrHeader
		}
		name := string(b[1 : 1+nameLen])
		if _, dup := headers[name]; dup {
			return nil, fmt.Errorf("%w: %q", ErrDuplicate, name)
		}
		b = b[1+nameLen:]
		typ := b[0]
		b = b[1:]
		value, rest, err := headerValue(typ, b)
		if err != nil {
			return nil, err
		}
		headers[name] = value
		b = rest
	}
	return headers, nil
}

// headerValue reads one typed value. The types this program does not read are
// skipped by their fixed width rather than refused, so a frame carrying a
// timestamp still yields its payload.
func headerValue(typ byte, b []byte) (any, []byte, error) {
	switch typ {
	case headerTrue:
		return true, b, nil
	case headerFalse:
		return false, b, nil
	case headerByte:
		if len(b) < 1 {
			return nil, nil, ErrHeader
		}
		return int8(b[0]), b[1:], nil
	case headerShort:
		if len(b) < 2 {
			return nil, nil, ErrHeader
		}
		return int16(binary.BigEndian.Uint16(b)), b[2:], nil
	case headerInt:
		if len(b) < 4 {
			return nil, nil, ErrHeader
		}
		return int32(binary.BigEndian.Uint32(b)), b[4:], nil
	case headerTimestamp, headerUUID:
		width := 8
		if typ == headerUUID {
			width = 16
		}
		if len(b) < width {
			return nil, nil, ErrHeader
		}
		return nil, b[width:], nil
	case headerBytes, headerString:
		if len(b) < 2 {
			return nil, nil, ErrHeader
		}
		n := int(binary.BigEndian.Uint16(b))
		if len(b) < 2+n {
			return nil, nil, ErrHeader
		}
		raw := b[2 : 2+n]
		if typ == headerBytes {
			out := make([]byte, n)
			copy(out, raw)
			return out, b[2+n:], nil
		}
		return string(raw), b[2+n:], nil
	}
	return nil, nil, fmt.Errorf("%w: unknown value type %d", ErrHeader, typ)
}

// Encode builds one frame with string headers. Nothing in a request path emits
// a frame; the tests and the fuzz target build their input with this, so the
// reader is checked against the same definition it parses.
func Encode(headers map[string]string, payload []byte) []byte {
	var h []byte
	for name, value := range headers {
		h = append(h, byte(len(name)))
		h = append(h, name...)
		h = append(h, headerString)
		h = binary.BigEndian.AppendUint16(h, uint16(len(value)))
		h = append(h, value...)
	}
	total := prelude + len(h) + len(payload) + 4
	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(h)))
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:8]))
	out = append(out, h...)
	out = append(out, payload...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:total-4]))
}
