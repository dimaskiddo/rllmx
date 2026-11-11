package cursor

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"io"
	"testing"
)

// gzipPayload compresses a payload the way the service does.
func gzipPayload(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return b.Bytes()
}

func TestFrameReaderDecodesFlagsAndTrailers(t *testing.T) {
	plain := frameBytes([]byte("hello"))
	packed := frameBytes(gzipPayload(t, "pressured"))
	// Mark the packed frame as compressed and the last one as a trailer.
	packed[0] = flagCompressed
	trailer := frameBytes([]byte(`{"error":{"code":"resource_exhausted"}}`))
	trailer[0] = flagTrailer

	r := &frameReader{r: bytes.NewReader(append(append(plain, packed...), trailer...))}
	first, err := r.next()
	if err != nil || string(first.payload) != "hello" || first.trailer {
		t.Fatalf("first frame = %q, trailer %v, err %v", first.payload, first.trailer, err)
	}
	second, err := r.next()
	if err != nil || string(second.payload) != "pressured" || second.trailer {
		t.Fatalf("second frame = %q, trailer %v, err %v", second.payload, second.trailer, err)
	}
	third, err := r.next()
	if err != nil || !third.trailer || !bytes.Contains(third.payload, []byte("resource_exhausted")) {
		t.Fatalf("trailer frame = %q, trailer %v, err %v", third.payload, third.trailer, err)
	}
	if _, err := r.next(); err != io.EOF {
		t.Fatalf("after the last frame: %v, want EOF", err)
	}
}

func TestFrameReaderRefusesAnOversizedLength(t *testing.T) {
	head := make([]byte, frameHeader)
	binary.BigEndian.PutUint32(head[1:], frameLimit+1)
	if _, err := (&frameReader{r: bytes.NewReader(head)}).next(); err != errFrame {
		t.Fatalf("oversized frame: %v, want errFrame", err)
	}
}

func TestDecompressKeepsAJSONErrorWhole(t *testing.T) {
	// A status body never carries the compressed flag's meaning, whatever the flag.
	body := []byte(`{"error":{"code":"unauthenticated"}}`)
	if got, err := decompress(body, flagTrailer); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("decompress = %q, %v", got, err)
	}
}

func FuzzFrameReader(f *testing.F) {
	f.Add(frameBytes([]byte("a")))
	f.Add([]byte{0x02, 0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, raw []byte) {
		r := &frameReader{r: bytes.NewReader(raw)}
		for i := 0; i < 8; i++ {
			if _, err := r.next(); err != nil {
				return
			}
		}
	})
}
