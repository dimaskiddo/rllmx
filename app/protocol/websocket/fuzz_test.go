package websocket

import (
	"bufio"
	"bytes"
	"net"
	"testing"
)

// sink is the server's end of a socket that takes whatever is written to it, so
// a ping the reader answers has somewhere to go.
func sink() net.Conn {
	a, b := net.Pipe()
	go func() {
		buf := make([]byte, 512)
		for {
			if _, err := b.Read(buf); err != nil {
				return
			}
		}
	}()
	return a
}

// FuzzReadText feeds the reader whatever a server sends. It must never panic,
// never hand back more than one message's bound, and never return a message
// together with an error.
func FuzzReadText(f *testing.F) {
	f.Add([]byte{0x81, 0x02, 'h', 'i'})
	f.Add([]byte{0x01, 0x01, 'a', 0x80, 0x01, 'b'})
	f.Add([]byte{0x89, 0x00, 0x81, 0x01, 'x'})
	f.Add([]byte{0x81, 0x7f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0x81, 0x80, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, wire []byte) {
		c := &Conn{conn: sink(), br: bufio.NewReader(bytes.NewReader(wire))}
		defer c.conn.Close()
		for i := 0; i < 8; i++ {
			msg, err := c.ReadText()
			if len(msg) > MaxMessage {
				t.Fatalf("a message of %d bytes passed the %d bound", len(msg), MaxMessage)
			}
			if err != nil {
				if msg != nil {
					t.Fatalf("a message came back with an error: %v", err)
				}
				return
			}
		}
	})
}
