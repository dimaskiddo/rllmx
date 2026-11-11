package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

// fallbackDelay is how long the first address gets before the guard starts the
// next one alongside it (RFC 6555 fast fallback). It matches the reference's
// 250 ms; net.Dialer's own delay is never reached, because the guard dials the
// addresses it checked rather than handing a host name back to it.
const fallbackDelay = 250 * time.Millisecond

// NewDialer is the SSRF guard. It resolves a name itself, keeps only permitted
// addresses and connects to the address it checked, so the checked address is
// the connected address: there is no window between the check and the dial in
// which a second DNS answer could point somewhere else.
func NewDialer(timeout time.Duration, allow *Allow) *Guard {
	return &Guard{dialer: &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}, allow: allow}
}

// Guard is a dialer whose every connection passed the address check. It carries
// the same knobs a net.Dialer does, so a transport can use it as its DialContext.
type Guard struct {
	dialer *net.Dialer
	allow  *Allow
	// lookup replaces the system resolver in tests.
	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	// dial replaces the socket dial in tests; nil uses the net.Dialer.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// dialContext is the socket dial the guard uses.
func (g *Guard) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if g.dial != nil {
		return g.dial(ctx, network, addr)
	}
	return g.dialer.DialContext(ctx, network, addr)
}

func (g *Guard) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if g.lookup != nil {
		return g.lookup(ctx, host)
	}
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Timeout is the dial timeout the guard was built with.
func (g *Guard) Timeout() time.Duration { return g.dialer.Timeout }

// targets resolves addr and returns the permitted IP literals to try, in the
// resolver's order. A name on the list skips the Internal check for every
// address it returns, which is what lets a listed private-network IdP work. A
// Never address is refused whatever the list says.
func (g *Guard) targets(ctx context.Context, addr string) (string, []string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", nil, ErrBlocked
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", nil, ErrBlocked
	}

	if ip, err := netip.ParseAddr(host); err == nil {
		if !permitted(ip, g.allow) {
			return "", nil, ErrBlocked
		}
		return addr, []string{net.JoinHostPort(embedded(ip).String(), port)}, nil
	}

	ips, err := g.resolve(ctx, host)
	if err != nil || len(ips) == 0 {
		return "", nil, errors.New("network: the host name did not resolve")
	}
	listed := g.allow.Permits(host)
	var out []string
	for _, ip := range ips {
		if Classify(ip) != Never && (listed || permitted(ip, g.allow)) {
			out = append(out, net.JoinHostPort(embedded(ip).String(), port))
		}
	}
	if len(out) == 0 {
		return "", nil, ErrBlocked
	}
	return addr, out, nil
}

// permitted applies the one rule every caller shares: a Never address is refused
// whatever the list says; an Internal one needs a list entry; Public always
// passes.
func permitted(ip netip.Addr, allow *Allow) bool {
	switch Classify(ip) {
	case Never:
		return false
	case Internal:
		return allow.PermitsAddr(ip)
	}
	return true
}

// DialContext checks the target, then dials the literal it checked. A name that
// resolves to several permitted addresses races them the way RFC 6555 says: the
// first address gets a head start, and the next joins while it is still pending,
// so a host whose IPv6 path is reset is served over IPv4 without waiting out
// that address's full timeout.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	original, targets, err := g.targets(ctx, addr)
	if err != nil {
		return nil, err
	}
	if len(targets) == 1 && targets[0] == original {
		return g.dialContext(ctx, network, targets[0])
	}
	return g.race(ctx, network, targets)
}

// race dials the targets with fast fallback: each one starts fallbackDelay after
// the one before it unless an earlier one has answered, the first success wins,
// and every loser is cancelled. The last error is returned when all fail. No
// goroutine outlives the call: the derived context is cancelled on return.
func (g *Guard) race(ctx context.Context, network string, targets []string) (net.Conn, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		conn net.Conn
		err  error
	}
	results := make(chan outcome, len(targets))
	for i, target := range targets {
		go func(i int, target string) {
			if i > 0 {
				t := time.NewTimer(time.Duration(i) * fallbackDelay)
				defer t.Stop()
				select {
				case <-t.C:
				case <-ctx.Done():
					results <- outcome{err: ctx.Err()}
					return
				}
			}
			conn, err := g.dialContext(ctx, network, target)
			results <- outcome{conn, err}
		}(i, target)
	}
	var last error
	for range targets {
		select {
		case r := <-results:
			if r.err == nil {
				return r.conn, nil
			}
			if errors.Is(r.err, ErrBlocked) {
				return nil, r.err
			}
			last = r.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, last
}

// String describes the guard without naming a target, for a start-up log line.
func (g *Guard) String() string {
	if g.allow.Len() == 0 {
		return "network: internal addresses refused"
	}
	return "network: " + strconv.Itoa(g.allow.Len()) + " allowed private entries"
}
