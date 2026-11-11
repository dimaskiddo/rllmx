package cursor

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
)

// Connect frames: a flags byte, a big-endian length, then the payload. The two
// flag bits this service uses say the payload is compressed and that it is the
// end-of-stream trailer carrying the RPC status.
const (
	flagCompressed = 0x01
	flagTrailer    = 0x02

	frameHeader = 5
	// frameLimit bounds one payload. A real answer is far smaller; the cap only
	// stops a broken length from reserving memory.
	frameLimit = 8 << 20
)

// errFrame means the stream carried something that is not a frame.
var errFrame = errors.New("cursor: the answer is not a Connect frame")

// frame is one decoded payload.
type frame struct {
	payload []byte
	trailer bool
}

// frameReader reads frames off a body, one at a time.
type frameReader struct {
	r io.Reader
}

// next returns the next frame: decompressed, and marked when it is the trailer.
// io.EOF means the body ended.
func (f *frameReader) next() (frame, error) {
	var head [frameHeader]byte
	if _, err := io.ReadFull(f.r, head[:]); err != nil {
		return frame{}, err
	}
	flags := head[0]
	n := binary.BigEndian.Uint32(head[1:])
	if n > frameLimit {
		return frame{}, errFrame
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(f.r, raw); err != nil {
		return frame{}, err
	}
	payload, err := decompress(raw, flags)
	if err != nil {
		return frame{}, err
	}
	return frame{payload: payload, trailer: flags&flagTrailer != 0}, nil
}

// decompress reads a payload the way the service writes it: raw unless the flag
// says otherwise, and then gzip, zlib or raw deflate, tried in that order
// because the reference found all three in the wild.
func decompress(payload []byte, flags byte) ([]byte, error) {
	if flags&(flagCompressed|flagTrailer) == 0 {
		return payload, nil
	}
	// A JSON status body is never compressed, whatever the flag says.
	if bytes.HasPrefix(payload, []byte(`{"error"`)) {
		return payload, nil
	}
	if out, err := readGzip(payload); err == nil {
		return out, nil
	}
	if out, err := readZlib(payload); err == nil {
		return out, nil
	}
	if out, err := readFlate(payload); err == nil {
		return out, nil
	}
	return payload, nil
}

// readGzip inflates a gzip body.
func readGzip(payload []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	return readAll(r)
}

// readZlib inflates a zlib body.
func readZlib(payload []byte) ([]byte, error) {
	r, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	return readAll(r)
}

// readFlate inflates a body with neither header.
func readFlate(payload []byte) ([]byte, error) {
	return readAll(flate.NewReader(bytes.NewReader(payload)))
}

// frameBytes writes one frame around a payload.
func frameBytes(payload []byte) []byte {
	out := make([]byte, frameHeader, frameHeader+len(payload))
	binary.BigEndian.PutUint32(out[1:], uint32(len(payload)))
	return append(out, payload...)
}

// readAll reads a decompressor to its end and closes it.
func readAll(r io.ReadCloser) ([]byte, error) {
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, frameLimit))
	if err != nil {
		return nil, err
	}
	if len(out) >= frameLimit {
		return nil, errFrame
	}
	return out, nil
}
