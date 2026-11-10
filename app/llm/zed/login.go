package zed

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"rllmx/app/store/dialect"
)

// The URL-safe base64 the public key travels in, padding kept.
var b64urlPadded = base64.RawURLEncoding

var (
	// ErrBadKey means the key pair could not be made.
	ErrBadKey = errors.New("zed: the sign-in key pair could not be made")
	// ErrBadCallback means the pasted callback names no account or token.
	ErrBadCallback = errors.New("zed: the pasted callback is not a sign-in answer")
	// ErrDecrypt means the answer could not be read with the login's key.
	ErrDecrypt = errors.New("zed: the sign-in answer could not be decrypted")
)

// SignIn is one login's key pair and its address. The private key never leaves
// this process: it is what makes the answer readable only here.
type SignIn struct {
	Private *rsa.PrivateKey
	// Public is the DER public key, URL-safe base64.
	Public string
	// SystemID names this machine to the service; it is made for the login, never
	// read from the host.
	SystemID string
}

// NewSignIn makes a key pair and the sign-in address for it. port is the local
// callback port the browser answers on.
func NewSignIn(port int) (SignIn, string, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return SignIn{}, "", ErrBadKey
	}
	der := x509.MarshalPKCS1PublicKey(&key.PublicKey)
	s := SignIn{Private: key, Public: b64urlPadded.EncodeToString(der), SystemID: dialect.NewID()}
	addr, err := s.Address(port)
	if err != nil {
		return SignIn{}, "", err
	}
	return s, addr, nil
}

// Decrypt reads the account's own token out of a callback. The service encrypts
// it with the public key; the private one, which never left this process, is the
// only thing that can read it.
func (s SignIn) Decrypt(encrypted string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encrypted, "="))
	if err != nil {
		return "", ErrDecrypt
	}
	out, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, s.Private, raw, nil)
	if err != nil {
		return "", ErrDecrypt
	}
	return validToken(out)
}

// validToken holds a decrypted answer to text a credential can be.
func validToken(b []byte) (string, error) {
	if len(b) == 0 || len(b) > 8<<10 || !utf8.Valid(b) {
		return "", ErrDecrypt
	}
	s := strings.TrimSpace(string(b))
	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", ErrDecrypt
	}
	return s, nil
}

// Callback is what the browser brought back.
type Callback struct {
	UserID    string
	Encrypted string
	FromJSON  bool
}

// ParseCallback reads the answer the browser handed over: the callback address,
// a bare query, or the JSON a proxy forwards. Only these two values are read;
// anything else in the text is ignored.
func ParseCallback(raw string) (Callback, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 16<<10 {
		return Callback{}, ErrBadCallback
	}
	if strings.HasPrefix(raw, "{") {
		var v map[string]any
		if json.Unmarshal([]byte(raw), &v) == nil {
			cb := Callback{
				UserID:    firstStringField(v, "user_id", "userId"),
				Encrypted: firstStringField(v, "access_token", "accessToken", "token"),
			}
			return checkCallback(cb)
		}
	}
	query := raw
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		query = raw[i+1:]
	}
	query = strings.TrimPrefix(query, "?")
	values, err := url.ParseQuery(query)
	if err != nil {
		return Callback{}, ErrBadCallback
	}
	return checkCallback(Callback{
		UserID:    firstValue(values, "user_id", "userId"),
		Encrypted: firstValue(values, "access_token", "accessToken", "token"),
	})
}

func firstValue(v url.Values, names ...string) string {
	for _, n := range names {
		if got := v.Get(n); got != "" {
			return got
		}
	}
	return ""
}

func firstStringField(v map[string]any, names ...string) string {
	for _, n := range names {
		if s, ok := v[n].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func checkCallback(cb Callback) (Callback, error) {
	if cb.UserID == "" || cb.Encrypted == "" || len(cb.UserID) > 256 || len(cb.Encrypted) > 8<<10 {
		return Callback{}, ErrBadCallback
	}
	return cb, nil
}

// Encode writes the login's private key as text the session store holds. It is
// never sent to the browser and never written to disk.
func (s SignIn) Encode() (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(s.Private)
	if err != nil {
		return "", ErrBadKey
	}
	return b64urlPadded.EncodeToString(der), nil
}

// DecodeSignIn reads a key a login kept, and its system id.
func DecodeSignIn(encoded, systemID string) (SignIn, error) {
	der, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded, "="))
	if err != nil {
		return SignIn{}, ErrBadKey
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return SignIn{}, ErrBadKey
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok || rsaKey.N.BitLen() < 2048 {
		return SignIn{}, ErrBadKey
	}
	return SignIn{Private: rsaKey, SystemID: systemID}, nil
}

// Address is the sign-in page for this key, naming the local callback port.
func (s SignIn) Address(port int) (string, error) {
	u, err := url.Parse(WebURL + "/native_app_signin")
	if err != nil {
		return "", ErrBadKey
	}
	q := u.Query()
	q.Set("native_app_port", strconv.Itoa(port))
	q.Set("native_app_public_key", s.Public)
	if s.SystemID != "" {
		q.Set("system_id", s.SystemID)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
