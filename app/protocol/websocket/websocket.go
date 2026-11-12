// Package websocket is the minimal client the streaming transcription path
// needs: one dial through the guarded network, text frames, ping/pong and a
// bounded message. It is ours because the available libraries cannot take the
// dialer the SSRF guard provides.
package websocket

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	// MaxMessage bounds one message, fragmented or not.
	MaxMessage = 1 << 20
	// handshakeTime bounds the opening exchange.
	handshakeTime = 15 * time.Second
	acceptGUID    = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
)

// ErrClosed means the peer closed the socket.
var ErrClosed = errors.New("websocket: closed by the peer")

// Conn is one open WebSocket.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

// Options are the caller's transport: its guarded dialer, its TLS settings and
// the dial timeout. The package imports nothing of the program, so the owner of
// the guard supplies them.
type Options struct {
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error)
	TLSConfig *tls.Config
	Timeout   time.Duration
}

// Dial opens a client connection. The target must be ws or wss.
func Dial(ctx context.Context, target string, header http.Header, opts Options) (*Conn, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || (u.Scheme != "wss" && u.Scheme != "ws") {
		return nil, errors.New("websocket: the address is not a ws or wss URL")
	}
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "ws" {
			port = "80"
		}
	}
	if opts.Dial == nil {
		return nil, errors.New("websocket: no dialer")
	}
	dctx, cancel := context.WithTimeout(ctx, opts.Timeout+handshakeTime)
	defer cancel()
	raw, err := opts.Dial(dctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if err != nil {
		return nil, err
	}
	if u.Scheme == "wss" {
		tlsConfig := opts.TLSConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		tlsConfig = tlsConfig.Clone()
		tlsConfig.ServerName = u.Hostname()
		raw = tls.Client(raw, tlsConfig)
	}
	keyRaw := make([]byte, 16)
	if _, err := rand.Read(keyRaw); err != nil {
		raw.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyRaw)
	path := u.RequestURI()
	host := u.Host
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", path, host, key)
	for k, vs := range header {
		for _, v := range vs {
			fmt.Fprintf(&b, "%s: %s\r\n", k, v)
		}
	}
	b.WriteString("\r\n")
	_ = raw.SetDeadline(time.Now().Add(handshakeTime))
	if _, err := io.WriteString(raw, b.String()); err != nil {
		raw.Close()
		return nil, err
	}
	br := bufio.NewReader(io.LimitReader(raw, 1<<16))
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		raw.Close()
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		raw.Close()
		return nil, fmt.Errorf("websocket: the server answered %d", resp.StatusCode)
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), "websocket") || !accepts(resp.Header.Get("Sec-WebSocket-Accept"), key) {
		raw.Close()
		return nil, errors.New("websocket: the server did not accept the handshake")
	}
	_ = raw.SetDeadline(time.Time{})
	return &Conn{conn: raw, br: bufio.NewReader(raw)}, nil
}

func accepts(got, key string) bool {
	sum := sha1.Sum([]byte(key + acceptGUID))
	return subtle.ConstantTimeCompare([]byte(got), []byte(base64.StdEncoding.EncodeToString(sum[:]))) == 1
}

// SetDeadline bounds the next read and write, for a turn timer.
func (c *Conn) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// WriteText sends one text message.
func (c *Conn) WriteText(payload []byte) error { return c.write(0x1, payload) }

// Ping sends a ping.
func (c *Conn) Ping(payload []byte) error { return c.write(0x9, payload) }

// Close closes the socket.
func (c *Conn) Close() error { return c.conn.Close() }

// ReadText returns the next text message. A close frame is ErrClosed; a ping is
// answered and reading continues.
func (c *Conn) ReadText() ([]byte, error) {
	var out []byte
	fragmented := false
	for {
		fin, opcode, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x1: // text
			if fragmented {
				return nil, errors.New("websocket: a new message inside a fragment")
			}
			if fin {
				return payload, nil
			}
			fragmented, out = true, payload
		case 0x0: // continuation
			if !fragmented {
				return nil, errors.New("websocket: a continuation with no message")
			}
			if len(out)+len(payload) > MaxMessage {
				return nil, errors.New("websocket: the message is too large")
			}
			out = append(out, payload...)
			if fin {
				return out, nil
			}
		case 0x8:
			return nil, ErrClosed
		case 0x9:
			if err := c.write(0xA, payload); err != nil {
				return nil, err
			}
		case 0xA:
		default:
			return nil, errors.New("websocket: unexpected frame")
		}
	}
}

func (c *Conn) write(opcode byte, payload []byte) error {
	if len(payload) > MaxMessage {
		return errors.New("websocket: the message is too large")
	}
	head := []byte{0x80 | opcode}
	n := len(payload)
	switch {
	case n < 126:
		head = append(head, 0x80|byte(n))
	case n <= 0xFFFF:
		head = append(head, 0x80|126, byte(n>>8), byte(n))
	default:
		head = append(head, 0x80|127, 0, 0, 0, 0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	key := make([]byte, 4)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	head = append(head, key...)
	masked := make([]byte, n)
	for i, b := range payload {
		masked[i] = b ^ key[i%4]
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if _, err := c.conn.Write(head); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

func (c *Conn) readFrame() (fin bool, opcode byte, payload []byte, err error) {
	h, err := c.br.ReadByte()
	if err != nil {
		return false, 0, nil, err
	}
	fin = h&0x80 != 0
	opcode = h & 0x0F
	mask, err := c.br.ReadByte()
	if err != nil {
		return false, 0, nil, err
	}
	length := int64(mask & 0x7F)
	switch length {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		length = int64(b[0])<<8 | int64(b[1])
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		length = 0
		for _, v := range b {
			length = length<<8 | int64(v)
		}
	}
	if length < 0 || length > MaxMessage {
		return false, 0, nil, errors.New("websocket: the frame is too large")
	}
	if mask&0x80 != 0 {
		return false, 0, nil, errors.New("websocket: the server masked a frame")
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	return fin, opcode, payload, nil
}
