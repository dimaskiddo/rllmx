package network

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	// A QUIC handshake is given a short deadline so a network that drops UDP
	// outright falls back to HTTP/2 without waiting out the connect timeout.
	http3Handshake = 3 * time.Second
	http3Idle      = 90 * time.Second
)

// quicTargets resolves addr through the guard and returns the permitted UDP
// destinations as literals, so the checked address is the connected address.
func quicTargets(ctx context.Context, g *Guard, addr string) ([]*net.UDPAddr, error) {
	_, targets, err := g.targets(ctx, addr)
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, ErrBlocked
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return nil, ErrBlocked
	}
	out := make([]*net.UDPAddr, 0, len(targets))
	for _, target := range targets {
		host, _, err := net.SplitHostPort(target)
		if err != nil {
			return nil, ErrBlocked
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return nil, ErrBlocked
		}
		out = append(out, &net.UDPAddr{IP: ip, Port: n})
	}
	return out, nil
}

// quicDialer is the guard's UDP path. One socket and one QUIC transport serve
// every connection this client opens.
type quicDialer struct {
	g    *Guard
	once sync.Once
	qt   *quic.Transport
	err  error
}

func (q *quicDialer) dial(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
	targets, err := quicTargets(ctx, q.g, addr)
	if err != nil {
		return nil, err
	}
	qt, err := q.transport()
	if err != nil {
		return nil, err
	}
	var last error
	for _, target := range targets {
		conn, err := qt.DialEarly(ctx, target, tlsCfg, cfg)
		if err == nil {
			return conn, nil
		}
		last = err
	}
	if last == nil {
		return nil, ErrBlocked
	}
	return nil, last
}

func (q *quicDialer) transport() (*quic.Transport, error) {
	q.once.Do(func() {
		conn, err := net.ListenUDP("udp", nil)
		if err != nil {
			q.err = err
			return
		}
		q.qt = &quic.Transport{Conn: conn}
	})
	return q.qt, q.err
}

func newHTTP3Transport(o Options, guard *Guard) *http3.Transport {
	cfg := newTLSConfig(o)
	// A QUIC ticket is not interchangeable with a TCP one, so this side keeps
	// its own cache even when the caller supplied one.
	cfg.ClientSessionCache = tls.NewLRUClientSessionCache(sessionCache)
	return &http3.Transport{
		TLSClientConfig: cfg,
		QUICConfig: &quic.Config{
			HandshakeIdleTimeout: http3Handshake,
			MaxIdleTimeout:       http3Idle,
			KeepAlivePeriod:      pingIdle,
		},
		Dial: (&quicDialer{g: guard}).dial,
	}
}
