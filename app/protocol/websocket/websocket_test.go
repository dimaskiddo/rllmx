package websocket

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// echoServer accepts one client and echoes text frames, answering a close.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		key := r.Header.Get("Sec-WebSocket-Key")
		sum := sha1.Sum([]byte(key + acceptGUID))
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
			base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
		_ = brw.Flush()
		br := bufio.NewReader(conn)
		for {
			fin, opcode, payload, err := readClientFrame(br)
			if err != nil {
				return
			}
			_ = fin
			switch opcode {
			case 0x1:
				writeServerFrame(conn, 0x1, payload)
			case 0x9:
				writeServerFrame(conn, 0xA, payload)
			case 0x8:
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readClientFrame(br *bufio.Reader) (bool, byte, []byte, error) {
	h, err := br.ReadByte()
	if err != nil {
		return false, 0, nil, err
	}
	fin, opcode := h&0x80 != 0, h&0x0F
	m, err := br.ReadByte()
	if err != nil {
		return false, 0, nil, err
	}
	n := int(m & 0x7F)
	if n == 126 {
		b := make([]byte, 2)
		if _, err := io.ReadFull(br, b); err != nil {
			return false, 0, nil, err
		}
		n = int(b[0])<<8 | int(b[1])
	}
	key := make([]byte, 4)
	if _, err := io.ReadFull(br, key); err != nil {
		return false, 0, nil, err
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(br, p); err != nil {
		return false, 0, nil, err
	}
	for i := range p {
		p[i] ^= key[i%4]
	}
	return fin, opcode, p, nil
}

func writeServerFrame(w net.Conn, opcode byte, payload []byte) {
	head := []byte{0x80 | opcode}
	if len(payload) < 126 {
		head = append(head, byte(len(payload)))
	} else {
		head = append(head, 126, byte(len(payload)>>8), byte(len(payload)))
	}
	_, _ = w.Write(append(head, payload...))
}

func TestATextMessageIsExchanged(t *testing.T) {
	srv := echoServer(t)
	opts := Options{Dial: (&net.Dialer{}).DialContext, Timeout: time.Second}
	conn, err := Dial(context.Background(), "ws://"+strings.TrimPrefix(srv.URL, "http://")+"/ws", nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteText([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := conn.ReadText()
	if err != nil || string(got) != "hello" {
		t.Fatalf("%q %v", got, err)
	}
	if err := conn.Ping(nil); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteText([]byte(strings.Repeat("x", 130))); err != nil {
		t.Fatal(err)
	}
	if got, err := conn.ReadText(); err != nil || len(got) != 130 {
		t.Fatalf("%d %v", len(got), err)
	}
}

func TestAnAddressThatIsNotAWebSocketIsRefused(t *testing.T) {
	if _, err := Dial(context.Background(), "https://example.com/x", nil, Options{}); err == nil {
		t.Fatal("accepted")
	}
}
