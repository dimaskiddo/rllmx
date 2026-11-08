package network

import (
	"errors"
	"net/netip"
	"strings"
)

// Class says whether an address may be dialled.
type Class int

const (
	// Public is every address outside the reserved ranges: always allowed.
	Public Class = iota
	// Internal is loopback, private, carrier-grade NAT, multicast, reserved or
	// unspecified: reachable only through the operator's list.
	Internal
	// Never is link-local and the cloud metadata address: refused even when the
	// operator lists it.
	Never
)

// ErrBlocked names no address, so a log line cannot leak the target.
var ErrBlocked = errors.New("network: the address is not permitted")

// neverPrefixes beat every allow-list entry. The metadata addresses are refused
// even when an operator lists them, because reaching one is a credential theft
// in cloud deployments, not a configuration choice.
var neverPrefixes = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
}

// metadataIPv6 is the AWS instance metadata endpoint on IPv6.
var metadataIPv6 = netip.MustParseAddr("fd00:ec2::254")

// internalPrefixes are the reserved ranges that need a list entry.
var internalPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	// Local-use NAT64: where the IPv4 address sits depends on the prefix length,
	// so the whole range needs a list entry instead of being decoded.
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// nat64 is the IPv6 range that carries an IPv4 address in its low 32 bits.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// compatible is the deprecated IPv4-compatible range, which also carries an
// IPv4 address in its low 32 bits.
var compatible = netip.MustParsePrefix("::/96")

// Classify judges an address. The order is invalid, then Never, then
// Internal against Public: Never must beat a list entry, which is the rule a
// check written in the other order gets wrong.
func Classify(a netip.Addr) Class {
	if !a.IsValid() {
		return Never
	}
	a = embedded(a)
	if a == metadataIPv6 {
		return Never
	}
	for _, p := range neverPrefixes {
		if p.Contains(a) {
			return Never
		}
	}
	if a.IsUnspecified() || isReservedOddity(a) {
		return Internal
	}
	for _, p := range internalPrefixes {
		if p.Contains(a) {
			return Internal
		}
	}
	return Public
}

// embedded reduces an IPv4-in-IPv6 address to the IPv4 it carries: the
// IPv4-mapped form, the NAT64 form and the deprecated compatible form all name
// an IPv4 target, and a guard that judged the wrapper would let them through.
func embedded(a netip.Addr) netip.Addr {
	a = a.Unmap().WithZone("")
	if !a.Is6() {
		return a
	}
	if nat64.Contains(a) || compatible.Contains(a) {
		b := a.As16()
		if v4, ok := netip.AddrFromSlice(b[12:16]); ok {
			return v4
		}
	}
	return a
}

// isReservedOddity covers the singular addresses netip does not fold into a
// prefix: the IPv6 unspecified address is handled by IsUnspecified, and this
// catches the IPv4 broadcast.
func isReservedOddity(a netip.Addr) bool {
	return a.Is4() && a == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// Allow is the operator's list: host names, single addresses and CIDR ranges.
// A nil Allow permits nothing internal, which is the default.
type Allow struct {
	hosts    map[string]bool
	prefixes []netip.Prefix
}

// ParseAllow reads a comma-separated list. An empty value returns nil, which
// permits no internal address at all. A bad entry returns an error that names no
// value, so a malformed secret in the list cannot reach a log.
func ParseAllow(raw string) (*Allow, error) {
	items := strings.Split(raw, ",")
	a := &Allow{hosts: map[string]bool{}}
	seen := false
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		seen = true
		if p, err := netip.ParsePrefix(item); err == nil {
			a.prefixes = append(a.prefixes, p)
			continue
		}
		if ip, err := netip.ParseAddr(item); err == nil {
			a.prefixes = append(a.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
			continue
		}
		if host, ok := hostName(item); ok {
			a.hosts[host] = true
			continue
		}
		return nil, errors.New("network: UPSTREAM_ALLOW_PRIVATE: an entry is not a host name, an address or a CIDR range")
	}
	if !seen {
		return nil, nil
	}
	return a, nil
}

// hostName accepts a DNS name this program is willing to match: the characters
// a host name may hold, at least one dot or a bare label such as localhost.
func hostName(item string) (string, bool) {
	h := normalizeHost(item)
	if h == "" || len(h) > 253 {
		return "", false
	}
	for label := range strings.SplitSeq(h, ".") {
		if label == "" || len(label) > 63 {
			return "", false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return "", false
			}
		}
	}
	return h, true
}

// Permits reports whether a host name is on the list. A listed name may resolve
// to any address, which is what makes a private-network IdP reachable.
func (a *Allow) Permits(host string) bool {
	if a == nil {
		return false
	}
	return a.hosts[normalizeHost(host)]
}

// PermitsAddr reports whether an address, or a range containing it, is listed.
func (a *Allow) PermitsAddr(ip netip.Addr) bool {
	if a == nil {
		return false
	}
	ip = embedded(ip)
	for _, p := range a.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Len reports how many entries the list holds, for a start-up summary.
func (a *Allow) Len() int {
	if a == nil {
		return 0
	}
	return len(a.hosts) + len(a.prefixes)
}

// normalizeHost lower-cases a name and drops one trailing dot, the same
// normalisation IsLoopbackHost applies, so the two agree on every spelling.
func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}
