package network

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// CheckPublic refuses a client-supplied address that is not plainly public: a
// scheme that is not http or https, user info, no host, a Never address, or a
// name with one answer the operator has not listed. It runs before a third party
// is asked to fetch the target, where this program's dialer never sees the
// connection, so the guard alone cannot cover it.
func CheckPublic(ctx context.Context, raw string, allow *Allow) error {
	return checkPublic(ctx, raw, allow, net.DefaultResolver.LookupNetIP)
}

func checkPublic(ctx context.Context, raw string, allow *Allow, lookup func(context.Context, string, string) ([]netip.Addr, error)) error {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("network: the address is not a public http or https URL")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		if !permitted(ip, allow) {
			return ErrBlocked
		}
		return nil
	}
	ips, err := lookup(ctx, "ip", u.Hostname())
	if err != nil || len(ips) == 0 {
		return errors.New("network: the host name did not resolve")
	}
	listed := allow.Permits(u.Hostname())
	for _, ip := range ips {
		if Classify(ip) == Never {
			return ErrBlocked
		}
		if !listed && !permitted(ip, allow) {
			return ErrBlocked
		}
	}
	return nil
}

// With returns the list extended by one host, for an operator-set address that
// is not on the outbound list: a local service this program is told to call. An
// IP literal is added as its own single-address range, because the dialer checks
// a literal against the ranges and never against the host names.
func (a *Allow) With(host string) *Allow {
	host = normalizeHost(host)
	if host == "" {
		return a
	}
	out := &Allow{hosts: map[string]bool{}}
	if a != nil {
		out.prefixes = append([]netip.Prefix(nil), a.prefixes...)
		for h := range a.hosts {
			out.hosts[h] = true
		}
	}
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		ip = embedded(ip)
		out.prefixes = append(out.prefixes, netip.PrefixFrom(ip, ip.BitLen()))
		return out
	}
	out.hosts[host] = true
	return out
}
