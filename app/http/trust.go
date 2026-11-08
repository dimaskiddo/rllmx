package http

import (
	nethttp "net/http"
	"net/netip"
	"slices"
	"strings"
)

// Trust knows which socket peers are reverse proxies whose forwarding headers
// are believed: loopback always, plus the configured addresses. The driver's
// own client IP trusts every peer, so nothing here uses it.
type Trust struct {
	proxies []netip.Prefix
}

// NewTrust adds the configured proxies to the loopback ranges.
func NewTrust(configured []netip.Prefix) Trust {
	loopback := []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	return Trust{proxies: append(loopback, configured...)}
}

func (t Trust) contains(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	for _, p := range t.proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func peerAddr(r *nethttp.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap().WithZone(""), true
}

// PeerTrusted reports whether the socket peer of r is a trusted proxy.
func (t Trust) PeerTrusted(r *nethttp.Request) bool {
	a, ok := peerAddr(r)
	return ok && t.contains(a)
}

// ClientAddr is the address of the caller for logs and the login limiter: the
// last X-Forwarded-For entry that is not itself a trusted proxy when the peer
// is trusted, else the socket peer. No access decision reads it.
func (t Trust) ClientAddr(r *nethttp.Request) string {
	peer, ok := peerAddr(r)
	if !ok {
		return r.RemoteAddr
	}
	if !t.contains(peer) {
		return peer.String()
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for _, hop := range slices.Backward(hops) {
		a, err := netip.ParseAddr(strings.TrimSpace(hop))
		if err != nil {
			return peer.String()
		}
		if !t.contains(a) {
			return a.Unmap().String()
		}
	}
	return peer.String()
}

// SecureRequest reports whether the caller reached us over HTTPS: TLS to this
// process, or a trusted proxy that says so in X-Forwarded-Proto. An untrusted
// peer cannot claim it.
func (t Trust) SecureRequest(r *nethttp.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !t.PeerTrusted(r) {
		return false
	}
	v := r.Header.Get("X-Forwarded-Proto")
	if i := strings.LastIndexByte(v, ','); i >= 0 {
		v = v[i+1:]
	}
	return strings.EqualFold(strings.TrimSpace(v), "https")
}
