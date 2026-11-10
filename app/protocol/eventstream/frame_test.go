package eventstream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"testing"
)

// frame builds one frame by hand, independent of Encode, so the reader is held
// to the layout rather than to its own writer.
func frame(t *testing.T, headers []string, payload []byte) []byte {
	t.Helper()
	var h []byte
	for i := 0; i+1 < len(headers); i += 2 {
		name, value := headers[i], headers[i+1]
		h = append(h, byte(len(name)))
		h = append(h, name...)
		h = append(h, headerString)
		h = binary.BigEndian.AppendUint16(h, uint16(len(value)))
		h = append(h, value...)
	}
	total := 16 + len(h) + len(payload)
	out := make([]byte, 0, total)
	out = binary.BigEndian.AppendUint32(out, uint32(total))
	out = binary.BigEndian.AppendUint32(out, uint32(len(h)))
	out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:8]))
	out = append(out, h...)
	out = append(out, payload...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(out[:total-4]))
}

func TestAFrameYieldsItsHeadersAndPayload(t *testing.T) {
	body := bytes.NewReader(append(
		frame(t, []string{":event-type", "chunk", ":message-type", "event"}, []byte(`{"bytes":"aGk="}`)),
		frame(t, []string{":event-type", "chunk"}, []byte(`{"bytes":"ho"}`))...))
	r := NewReader(body)

	first, err := r.Next()
	if err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if got := first.Type(":event-type"); got != "chunk" {
		t.Errorf("event type %q", got)
	}
	if got := first.Type(":message-type"); got != "event" {
		t.Errorf("message type %q", got)
	}
	if !bytes.Equal(first.Payload, []byte(`{"bytes":"aGk="}`)) {
		t.Errorf("payload %q", first.Payload)
	}
	if _, err := r.Next(); err != nil {
		t.Fatalf("second frame: %v", err)
	}
	if _, err := r.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("end: %v, want io.EOF", err)
	}
}

func TestAnEmptyPayloadIsNoPayload(t *testing.T) {
	f, err := NewReader(bytes.NewReader(frame(t, []string{":event-type", "chunk"}, nil))).Next()
	if err != nil {
		t.Fatalf("frame: %v", err)
	}
	if f.Payload != nil {
		t.Errorf("payload %q, want nil", f.Payload)
	}
}

func TestEveryIntegrityFailureIsRefused(t *testing.T) {
	good := frame(t, []string{":event-type", "chunk"}, []byte(`{}`))
	corrupt := func(fn func([]byte)) []byte {
		b := append([]byte(nil), good...)
		fn(b)
		return b
	}
	cases := map[string]struct {
		body []byte
		want error
	}{
		"prelude crc":   {corrupt(func(b []byte) { b[8] ^= 0xff }), ErrPrelude},
		"message crc":   {corrupt(func(b []byte) { b[len(b)-1] ^= 0xff }), ErrMessage},
		"zero length":   {corrupt(func(b []byte) { binary.BigEndian.PutUint32(b[0:4], 0) }), ErrBounds},
		"over the cap":  {corrupt(func(b []byte) { binary.BigEndian.PutUint32(b[0:4], 25<<20) }), ErrBounds},
		"headers past":  {corrupt(func(b []byte) { binary.BigEndian.PutUint32(b[4:8], uint32(len(b))) }), ErrBounds},
		"truncated":     {good[:len(good)-1], io.ErrUnexpectedEOF},
		"missing bytes": {good[:4], io.ErrUnexpectedEOF},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewReader(bytes.NewReader(tc.body)).Next()
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestADuplicateHeaderIsRefused(t *testing.T) {
	body := frame(t, []string{"a", "1", "a", "2"}, nil)
	if _, err := NewReader(bytes.NewReader(body)).Next(); !errors.Is(err, ErrDuplicate) {
		t.Errorf("got %v, want ErrDuplicate", err)
	}
}

// TestAHeaderNameLongerThanItsBlockIsRefused covers the one place a length byte
// could walk off the header block.
func TestAHeaderNameLongerThanItsBlockIsRefused(t *testing.T) {
	h := []byte{200, 'a', 'b', 'c'}
	total := 16 + len(h)
	body := make([]byte, 0, total)
	body = binary.BigEndian.AppendUint32(body, uint32(total))
	body = binary.BigEndian.AppendUint32(body, uint32(len(h)))
	body = binary.BigEndian.AppendUint32(body, crc32.ChecksumIEEE(body[:8]))
	body = append(body, h...)
	body = binary.BigEndian.AppendUint32(body, crc32.ChecksumIEEE(body[:total-4]))

	if _, err := NewReader(bytes.NewReader(body)).Next(); !errors.Is(err, ErrHeader) {
		t.Errorf("got %v, want ErrHeader", err)
	}
}

func TestEncodeWritesWhatTheReaderReads(t *testing.T) {
	payload := []byte(`{"a":1}`)
	for name, headers := range map[string]map[string]string{
		"one":  {":event-type": "chunk"},
		"none": {},
		"many": {":event-type": "chunk", ":message-type": "event", "x": "y"},
	} {
		t.Run(name, func(t *testing.T) {
			f, err := NewReader(bytes.NewReader(Encode(headers, payload))).Next()
			if err != nil {
				t.Fatalf("frame: %v", err)
			}
			for k, v := range headers {
				if got := f.Type(k); got != v {
					t.Errorf("header %q = %q, want %q", k, got, v)
				}
			}
			if !bytes.Equal(f.Payload, payload) {
				t.Errorf("payload %q", f.Payload)
			}
		})
	}
}

func FuzzFrame(f *testing.F) {
	f.Add(Encode(map[string]string{":event-type": "chunk"}, []byte(`{"bytes":"aGk="}`)))
	f.Add([]byte{})
	f.Add(make([]byte, 16))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := NewReader(bytes.NewReader(data))
		for range 4 {
			fr, err := r.Next()
			if err != nil {
				return
			}
			if len(fr.Payload) > DefaultBounds.MaxMessage {
				t.Fatalf("payload of %d bytes passed the cap", len(fr.Payload))
			}
			if data := fr.Payload; data != nil && len(data) == 0 {
				t.Fatal("a zero-length payload was returned as a value")
			}
		}
	})
}
