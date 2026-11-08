package http

import (
	nethttp "net/http"
	"net/netip"
	"strings"

	"rllmx/app/network"
)

// proxyHeaders mark a request that passed through something other than the
// client's own connection. Any one of them, even empty, makes it not local.
var proxyHeaders = []string{
	"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Real-Ip", "Forwarded", "Via",
	"Cf-Connecting-Ip", "True-Client-Ip", "X-Client-Ip", "X-Cluster-Client-Ip",
}

// loopbackPeer reports whether a "host:port" socket address is a loopback IP.
func loopbackPeer(remote string) bool {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return false
	}
	a := ap.Addr()
	return a.IsLoopback() && a.Zone() == ""
}

// splitHost removes the port from a Host header value and the brackets from an
// IPv6 literal. It refuses anything with userinfo or a malformed port.
func splitHost(hostport string) (string, bool) {
	if hostport == "" || strings.ContainsAny(hostport, "@/\\ \t%") {
		return "", false
	}
	if strings.HasPrefix(hostport, "[") {
		end := strings.Index(hostport, "]")
		if end < 0 {
			return "", false
		}
		rest := hostport[end+1:]
		if rest != "" && !validPortSuffix(rest) {
			return "", false
		}
		return hostport[1:end], true
	}
	if strings.Count(hostport, ":") > 1 {
		return hostport, true
	}
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		if !validPortSuffix(hostport[i:]) {
			return "", false
		}
		return hostport[:i], true
	}
	return hostport, true
}

func validPortSuffix(s string) bool {
	if len(s) < 2 || s[0] != ':' || len(s) > 6 {
		return false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// loopbackHost reports whether a Host header names this machine. A rebinding
// attack reaches the server with the attacker's own host name, which fails here.
func loopbackHost(hostport string) bool {
	h, ok := splitHost(hostport)
	return ok && network.IsLoopbackHost(h)
}

// loopbackOrigin reports whether an Origin header is a loopback web origin.
// "null" (sandboxed pages, file:// and redirects) is never local.
func loopbackOrigin(origin string) bool {
	rest, ok := originHost(origin)
	return ok && loopbackHost(rest)
}

func originHost(origin string) (string, bool) {
	l := strings.ToLower(origin)
	switch {
	case strings.HasPrefix(l, "http://"):
		return l[len("http://"):], true
	case strings.HasPrefix(l, "https://"):
		return l[len("https://"):], true
	}
	return "", false
}

func headerPresent(h nethttp.Header, name string) bool {
	_, ok := h[nethttp.CanonicalHeaderKey(name)]
	return ok
}

// IsLocalRequest decides whether a request came from a program or browser on
// this machine. It is the guard's own rule, exported for the routes that behave
// differently for a local caller.
func IsLocalRequest(r *nethttp.Request, trustLoopback bool) bool {
	return isLocalRequest(r, trustLoopback)
}

// isLocalRequest decides from the socket peer and the request headers whether
// a request came from a program or browser on this machine itself. It never
// reads the framework's client IP. With trustLoopback off, nothing is local.
func isLocalRequest(r *nethttp.Request, trustLoopback bool) bool {
	if !trustLoopback || !loopbackPeer(r.RemoteAddr) {
		return false
	}
	for _, name := range proxyHeaders {
		if headerPresent(r.Header, name) {
			return false
		}
	}
	if !loopbackHost(r.Host) {
		return false
	}
	if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) > 1 || !loopbackOrigin(origins[0])) {
		return false
	}
	if sites := r.Header.Values("Sec-Fetch-Site"); len(sites) > 0 {
		if len(sites) > 1 {
			return false
		}
		switch strings.ToLower(sites[0]) {
		case "same-origin", "none":
		default:
			return false
		}
	}
	return true
}
