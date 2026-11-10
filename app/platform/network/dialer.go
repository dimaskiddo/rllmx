package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"time"
)

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
// resolves to several permitted addresses is tried in turn, so a host with both
// an IPv6 and an IPv4 answer works when only one of them listens.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	original, targets, err := g.targets(ctx, addr)
	if err != nil {
		return nil, err
	}
	if len(targets) == 1 && targets[0] == original {
		return g.dialer.DialContext(ctx, network, targets[0])
	}
	var last error
	for _, target := range targets {
		conn, err := g.dialer.DialContext(ctx, network, target)
		if err == nil {
			return conn, nil
		}
		if errors.Is(err, ErrBlocked) {
			return nil, err
		}
		last = err
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
