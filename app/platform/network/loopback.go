// Package network holds the outbound and address helpers that more than one
// package needs. The SSRF dialer guard and the guarded clients join it with the
// provider core.
package network

import (
	"net/netip"
	"strings"
)

// IsLoopbackHost reports whether host names this machine: localhost or a
// loopback address. A trailing dot, one bracket pair and an IPv4-mapped form
// are tolerated; a zoned address is not, because its zone picks the interface.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(host, ".")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if n := len(host); n >= 2 && host[0] == '[' && host[n-1] == ']' {
		host = host[1 : n-1]
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Zone() == "" && a.IsLoopback()
}
