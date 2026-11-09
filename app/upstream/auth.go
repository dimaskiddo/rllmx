package upstream

import (
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Key schemes a provider's probe can use.
const (
	SchemeBearer = "bearer"
	SchemeRaw    = "raw"
	SchemeQuery  = "query"
	// SchemeWorkOS is a bearer token that carries the `workos:` marker when it is
	// a WorkOS access token, and goes unchanged when it is an API key.
	SchemeWorkOS = "workos"
)

const (
	maxAuthHeaders = 20
	maxHeaderBytes = 8192
)

// Auth says how a pasted key is presented to a provider when the key is checked.
// A provider without one cannot be checked by the generic prober.
type Auth struct {
	// Scheme is bearer (Authorization: Bearer key), raw (the key as the header's
	// value) or query (the key as a query parameter).
	Scheme string `json:"scheme"`
	// Header is the header name, or for the query scheme the parameter name.
	Header string `json:"header"`
	// ValidateURL is the address the probe asks. Empty means /models beside the
	// transport address.
	ValidateURL string `json:"validateUrl,omitempty"`
	// Headers are static headers sent with the probe. None of them is a
	// credential header: the key is only ever set by Scheme.
	Headers map[string]string `json:"headers,omitempty"`
	// BadKey lists the statuses that mean the key is wrong. Empty means 401, 403.
	BadKey []int `json:"badKey,omitempty"`
}

// ReservedHeader reports a header that no data file or override may set: the
// ones that frame a request or carry a credential.
func ReservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "host", "content-length", "content-type", "connection", "transfer-encoding", "authorization", "cookie":
		return true
	}
	return false
}

// HeaderName reports a valid header name: letters, digits and dashes.
func HeaderName(n string) bool {
	if n == "" || len(n) > 128 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// RequestHeaders returns the headers a request to the provider carries under
// this block: the static ones, then the key as the scheme says. The query scheme
// puts the key in the address instead, see WithKey.
func (a *Auth) RequestHeaders(key string) map[string]string {
	h := make(map[string]string, len(a.Headers)+1)
	for k, v := range a.Headers {
		h[k] = v
	}
	switch a.Scheme {
	case SchemeBearer:
		h[a.Header] = "Bearer " + key
	case SchemeWorkOS:
		h[a.Header] = "Bearer " + WorkOSToken(key)
	case SchemeRaw:
		h[a.Header] = key
	}
	return h
}

// workOSJWT is the start of a WorkOS access token: a JWT whose header is
// base64url JSON.
var workOSJWT = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// WorkOSToken is the credential as the provider reads it: a WorkOS access token
// with the `workos:` marker in front, anything else (a plan's API key) as it is.
func WorkOSToken(key string) string {
	key = strings.TrimSpace(key)
	if !workOSJWT.MatchString(key) {
		return key
	}
	return "workos:" + key
}

// WithKey puts the key into target as a query parameter for the query scheme
// and returns any other target as it was.
func (a *Auth) WithKey(target, key string) string {
	if a.Scheme != SchemeQuery {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	q := u.Query()
	q.Set(a.Header, key)
	u.RawQuery = q.Encode()
	return u.String()
}

// BadKeyStatus reports whether a status means the key was refused.
func (a *Auth) BadKeyStatus(code int) bool {
	if len(a.BadKey) == 0 {
		return code == 401 || code == 403
	}
	return slices.Contains(a.BadKey, code)
}

// validate checks an auth block against the provider's transport address.
func (a *Auth) validate(transportURL string) error {
	switch a.Scheme {
	case SchemeBearer, SchemeRaw, SchemeQuery, SchemeWorkOS:
	default:
		return errors.New("auth.scheme is unknown")
	}
	if !HeaderName(a.Header) {
		return errors.New("auth.header is not a valid name")
	}
	if a.Scheme != SchemeQuery && ReservedHeader(a.Header) && !((a.Scheme == SchemeBearer || a.Scheme == SchemeWorkOS) && strings.EqualFold(a.Header, "authorization")) {
		return errors.New("auth.header names a reserved header")
	}
	if len(a.Headers) > maxAuthHeaders {
		return errors.New("auth.headers has too many entries")
	}
	for name, value := range a.Headers {
		switch {
		case !HeaderName(name):
			return errors.New("auth.headers has an invalid name")
		case ReservedHeader(name) || strings.EqualFold(name, a.Header):
			return errors.New("auth.headers names a credential or framing header")
		case len(value) > maxHeaderBytes || strings.IndexFunc(value, unicode.IsControl) >= 0:
			return errors.New("auth.headers has an invalid value")
		}
	}
	for _, c := range a.BadKey {
		if c < 400 || c > 599 {
			return errors.New("auth.badKey holds a status outside 400-599")
		}
	}
	if a.ValidateURL == "" {
		return nil
	}
	return sameHost(a.ValidateURL, transportURL)
}

// sameHost holds a probe address to the transport's own host, so a data file
// cannot send a typed key to a third party.
func sameHost(probe, transport string) error {
	p, err := url.Parse(probe)
	if err != nil || p.Host == "" || p.User != nil || p.RawQuery != "" || p.Fragment != "" || (p.Scheme != "https" && p.Scheme != "http") {
		return errors.New("auth.validateUrl is not a plain http or https address")
	}
	t, err := url.Parse(transport)
	if err != nil || !strings.EqualFold(p.Host, t.Host) {
		return errors.New("auth.validateUrl names another host than the transport")
	}
	return nil
}

// ProbeURL is the address a key check asks: the data file's own, else /models
// beside the transport address.
func (e Entry) ProbeURL() string {
	if e.Auth != nil && e.Auth.ValidateURL != "" {
		return e.Auth.ValidateURL
	}
	u, err := url.Parse(e.Transport.BaseURL)
	if err != nil {
		return ""
	}
	p := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/chat/completions", "/responses", "/messages", "/embeddings"} {
		if strings.HasSuffix(p, suffix) {
			p = strings.TrimSuffix(p, suffix)
			break
		}
	}
	u.Path, u.RawQuery, u.Fragment = p+"/models", "", ""
	return u.String()
}
