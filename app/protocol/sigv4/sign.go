// Package sigv4 signs one HTTP request with AWS Signature Version 4. It is a
// protocol package: it knows a method, an address, headers and a key, not a
// provider.
//
// Every request this program signs is a single POST with a JSON body and no
// query string, so the cases that make SigV4 error-prone elsewhere (several
// values under one header, query ordering) are refused rather than
// half-implemented.
package sigv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	// Algorithm is the algorithm's name in the authorization header.
	Algorithm = "AWS4-HMAC-SHA256"
	// Terminator ends the credential scope.
	Terminator = "aws4_request"
	// DateHeader carries the signing instant.
	DateHeader = "x-amz-date"
	// TokenHeader carries a temporary credential's session token, which must be
	// signed as well as sent.
	TokenHeader = "x-amz-security-token"
)

var (
	// ErrNoKey means one half of the key pair is missing.
	ErrNoKey = errors.New("sigv4: both an access key and a secret are required")
	// ErrQuery means the address carries a query string, which this signer does
	// not canonicalise.
	ErrQuery = errors.New("sigv4: a query string is not signed")
	// ErrAddress means the address is not an absolute URL.
	ErrAddress = errors.New("sigv4: the address is not an absolute URL")
)

// Request is one call to sign.
type Request struct {
	Method string
	// URL is the absolute address, already escaped once.
	URL string
	// Header carries what will be sent; it is filled in place.
	Header http.Header
	Body   []byte
	Region string
	// Service is the AWS service the credential scope names, e.g. "bedrock".
	Service      string
	AccessKey    string
	SecretKey    string
	SessionToken string
	// Now is the signing instant; the zero value means time.Now().
	Now func() time.Time
	// SingleEncodePath skips the second escaping every service but S3 wants.
	SingleEncodePath bool
}

// Sign adds the headers the signature needs, including Authorization, and
// returns the set to send.
func Sign(r Request) (http.Header, error) {
	if r.AccessKey == "" || r.SecretKey == "" {
		return nil, ErrNoKey
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, ErrAddress
	}
	if u.RawQuery != "" {
		return nil, ErrQuery
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	amzDate, dateStamp := stamp(now())

	h := r.Header
	if h == nil {
		h = http.Header{}
	}
	h.Set(DateHeader, amzDate)
	if r.SessionToken != "" {
		h.Set(TokenHeader, r.SessionToken)
	}
	// host is signed but is not a request header the transport sends from here:
	// it comes from the address, so it is added to the set to sign only.
	signSet := map[string][]string{"host": {u.Host}}
	for name, values := range h {
		signSet[strings.ToLower(name)] = values
	}
	canonicalHeaders, signedHeaders := canonicalHeaders(signSet)

	canonical := strings.Join([]string{
		strings.ToUpper(r.Method),
		canonicalPath(u, !r.SingleEncodePath),
		"",
		canonicalHeaders,
		signedHeaders,
		hashHex(r.Body),
	}, "\n")

	scope := dateStamp + "/" + r.Region + "/" + r.Service + "/" + Terminator
	toSign := strings.Join([]string{Algorithm, amzDate, scope, hashHex([]byte(canonical))}, "\n")

	key := signingKey(dateStamp, r.Region, r.Service, r.SecretKey)
	signature := hex.EncodeToString(hmacSHA256(key, []byte(toSign)))

	h.Set("Authorization", Algorithm+" Credential="+r.AccessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	return h, nil
}

// stamp gives both forms of one instant: the compact one SigV4 signs and the
// date that scopes the key.
func stamp(t time.Time) (amzDate, dateStamp string) {
	t = t.UTC()
	amzDate = t.Format("20060102T150405Z")
	return amzDate, amzDate[:8]
}

// canonicalPath escapes each path segment a second time, which is what every
// service but S3 expects: a model id that reaches the wire as "%3A0" is signed
// as "%253A0".
func canonicalPath(u *url.URL, double bool) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	if !double {
		return p
	}
	segments := strings.Split(p, "/")
	for i, s := range segments {
		segments[i] = Escape(s)
	}
	return strings.Join(segments, "/")
}

// Escape percent-encodes everything outside the unreserved set. It is stricter
// than url.PathEscape, which leaves a colon alone and so would put a model's
// ":0" version suffix on the wire unescaped.
func Escape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte("0123456789ABCDEF"[c>>4])
			b.WriteByte("0123456789ABCDEF"[c&0xF])
		}
	}
	return b.String()
}

// canonicalHeaders lower-cases the names, trims the values with their internal
// whitespace runs collapsed, and sorts both by name.
func canonicalHeaders(set map[string][]string) (canonical, signed string) {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(strings.Join(collapse(set[name]), ","))
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(names, ";")
}

// collapse trims each value and folds its whitespace runs to one space, which
// is what AWS does before signing.
func collapse(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

// signingKey is the key the signature is made with: the secret, then the date,
// the region, the service and the terminator, each folded in by HMAC.
func signingKey(dateStamp, region, service, secret string) []byte {
	key := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	key = hmacSHA256(key, []byte(region))
	key = hmacSHA256(key, []byte(service))
	return hmacSHA256(key, []byte(Terminator))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func hashHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
