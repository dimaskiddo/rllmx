// Package sso holds what both single sign-on types share: the public base URL a
// redirect is built from, the guarded identity-provider client, the single-use
// store a sign-in lives in, the allow list and the fixed error codes.
package sso

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/url"
	"strings"

	nethttp "net/http"

	"rllmx/app/platform/network"
	"rllmx/app/platform/settings"
)

// PublicBase is the address a browser reaches this gateway at, used to build the
// redirect URI. APP_URL wins. Without it only a local request may use its own
// host, and only when that host names this machine, because a remote caller
// controls its Host header. A forwarded header never builds a URL.
func PublicBase(server *settings.Server, r *nethttp.Request, local bool) (string, bool) {
	if server != nil && server.URL != "" {
		return server.URL, true
	}
	if !local || server == nil {
		return "", false
	}
	host, ok := cleanHost(r.Host)
	if !ok {
		return "", false
	}
	return "http://" + host, true
}

// cleanHost accepts a host:port whose name is this machine, so a rebinding name
// cannot become the redirect URI.
func cleanHost(hostport string) (string, bool) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" || strings.ContainsAny(hostport, "@/\\ \t") {
		return "", false
	}
	u, err := url.Parse("http://" + hostport)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return "", false
	}
	if !network.IsLoopbackHost(u.Hostname()) {
		return "", false
	}
	return u.Host, true
}

// CleanURL joins a base and a path with exactly one slash.
func CleanURL(base, path string) string {
	return strings.TrimSuffix(base, "/") + "/" + strings.TrimPrefix(path, "/")
}

// BindCookie names the cookie that ties a callback to the browser that started
// the sign-in.
const BindCookie = "oidc_bind"

// BindTTL is how long a sign-in may take: ten minutes.
const BindTTL = 10 * 60

// Bind is the HMAC of a flow state under the session secret. It returns with the
// callback, so an attacker cannot plant their own code and state in a victim's
// browser: the state is in the URL, the MAC is in the cookie, and both must
// arrive together.
func Bind(secret []byte, state string) string { return BindFor(secret, BindCookie, state) }

// BindFor is Bind for a named binding, so the two sign-in types cannot be made
// to accept each other's cookie. With no secret it returns "", which no cookie
// can match: a MAC under an empty key is one anybody can compute.
func BindFor(secret []byte, name, state string) string {
	if len(secret) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(name))
	mac.Write([]byte("|"))
	mac.Write([]byte(state))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// BindMatches compares the cookie against the state in constant time.
func BindMatches(secret []byte, state, cookie string) bool {
	return BindMatchesFor(secret, BindCookie, state, cookie)
}

// BindMatchesFor is BindMatches for a named binding.
func BindMatchesFor(secret []byte, name, state, cookie string) bool {
	if len(secret) == 0 || state == "" || cookie == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(BindFor(secret, name, state)), []byte(cookie)) == 1
}
