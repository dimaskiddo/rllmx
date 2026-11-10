// Package cosy signs one Qoder request: the body is encoded for the
// gateway's own decoder, and the headers carry a wrapped key, an encrypted
// identity and a digest over the parts the gateway checks.
//
// It is a protocol package: it knows a body, an address and a credential, not a
// provider. The public key it wraps with is the vendor's own, so it is data
// rather than a literal here.
package cosy

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
)

// The alphabet the encoded body is written in. It is the gateway's own, so the
// encoder and its decoder must agree exactly.
const (
	stdAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	customAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
)

var (
	// ErrKey means the public key is not an RSA key this package can use.
	ErrKey = errors.New("cosy: the public key is not usable")
	// ErrAddress means the address is not absolute.
	ErrAddress = errors.New("cosy: the address is not absolute")
)

// ParseKey reads a PEM public key and holds it to a shape a wrap can use.
func ParseKey(pemText string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, ErrKey
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, ErrKey
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok || rsaKey.N.BitLen() < 1024 {
		return nil, ErrKey
	}
	return rsaKey, nil
}

// Encode writes a body in the gateway's own alphabet. A body of at least three
// bytes is rotated so its head moves to the end.
func Encode(plain []byte) string {
	std := base64.StdEncoding.EncodeToString(plain)
	n := len(std)
	a := n / 3
	rotated := std[n-a:] + std[a:n-a] + std[:a]
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = substitute(rotated[i])
	}
	return string(out)
}

// Decode reverses Encode. It exists for the tests and for a reader that wants to
// see what was sent; nothing in a request path calls it. A string it cannot
// reverse comes back as an error rather than as bytes.
func Decode(encoded string) ([]byte, error) {
	out := make([]byte, len(encoded))
	for i := 0; i < len(encoded); i++ {
		out[i] = unsubstitute(encoded[i])
	}
	rotated := string(out)
	n := len(rotated)
	a := n / 3
	if a > 0 {
		// [tail][mid][head] back to [head][mid][tail]. The encoded string holds
		// a characters of tail, then the middle, then a of head.
		tail := rotated[:a]
		mid := rotated[a : n-a]
		head := rotated[n-a:]
		rotated = head + mid + tail
	}
	return base64.StdEncoding.DecodeString(rotated)
}

// substitute maps one standard base64 character to the gateway's alphabet.
func substitute(c byte) byte {
	if i := strings.IndexByte(stdAlphabet, c); i >= 0 {
		return customAlphabet[i]
	}
	if c == '=' {
		return '$'
	}
	return c
}

// unsubstitute is the inverse of substitute.
func unsubstitute(c byte) byte {
	if i := strings.IndexByte(customAlphabet, c); i >= 0 {
		return stdAlphabet[i]
	}
	if c == '$' {
		return '='
	}
	return c
}

// Identity is who a request belongs to, as the gateway reads it.
type Identity struct {
	UserID, Token, Name, Email, MachineID string
	// IDEVersion, ClientType, MachineOS and MachineType are the client's own
	// constants, which the gateway compares against the values it signed with.
	IDEVersion, ClientType, MachineOS, MachineType string
}

// Headers is the signed header set for one request.
func Headers(public *rsa.PublicKey, target string, body []byte, id Identity) (map[string]string, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" {
		return nil, ErrAddress
	}
	if id.UserID == "" || id.Token == "" || public == nil {
		return nil, ErrKey
	}
	info, cosyKey, err := wrap(public, id)
	if err != nil {
		return nil, err
	}
	requestID, err := uuid()
	if err != nil {
		return nil, ErrKey
	}
	payload, err := jsonPayload(info, cosyKey, requestID, id.IDEVersion)
	if err != nil {
		return nil, ErrKey
	}
	payloadB64 := base64.StdEncoding.EncodeToString(payload)
	sigPath := sigPathOf(u.Path)
	sum := md5.Sum([]byte(payloadB64 + "\n" + cosyKey + "\n" + timestampNow() + "\n" + string(body) + "\n" + sigPath))
	bodySum := md5.Sum(body)
	h := map[string]string{
		"Authorization":          "Bearer COSY." + payloadB64 + "." + hex.EncodeToString(sum[:]),
		"Cosy-Key":               cosyKey,
		"Cosy-User":              id.UserID,
		"Cosy-Date":              timestampNow(),
		"Cosy-Version":           id.IDEVersion,
		"Cosy-Machineid":         id.MachineID,
		"Cosy-Machinetoken":      id.MachineID,
		"Cosy-Machinetype":       id.MachineType,
		"Cosy-Machineos":         id.MachineOS,
		"Cosy-Clienttype":        id.ClientType,
		"Cosy-Clientip":          "127.0.0.1",
		"Cosy-Bodyhash":          hex.EncodeToString(bodySum[:]),
		"Cosy-Bodylength":        itoa(len(body)),
		"Cosy-Sigpath":           sigPath,
		"Cosy-Data-Policy":       "disagree",
		"Cosy-Organization-Id":   "",
		"Cosy-Organization-Tags": "",
		"Login-Version":          "v2",
		"X-Request-Id":           requestID,
	}
	return h, nil
}

// wrap encrypts the identity under a fresh AES key, with the key itself wrapped
// by the vendor's public key.
func wrap(public *rsa.PublicKey, id Identity) (info, cosyKey string, err error) {
	key, err := aesKey()
	if err != nil {
		return "", "", ErrKey
	}
	plain := jsonIdentity(id)
	block, err := aes.NewCipher([]byte(key))
	if err != nil {
		return "", "", ErrKey
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	padded := append([]byte(plain), bytesOf(byte(pad), pad)...)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, []byte(key)).CryptBlocks(out, padded)
	// The gateway unwraps with PKCS#1 v1.5 and accepts nothing else, so the
	// padding is the peer's protocol rather than a choice this package makes.
	// The key being wrapped is a throwaway AES key, not a long-lived secret.
	//
	//lint:ignore SA1019 the peer's protocol fixes the padding
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, public, []byte(key))
	if err != nil {
		return "", "", ErrKey
	}
	return base64.StdEncoding.EncodeToString(out), base64.StdEncoding.EncodeToString(wrapped), nil
}

// aesKey is sixteen bytes: the first sixteen characters of a fresh identifier,
// exactly as the client makes them.
func aesKey() (string, error) {
	id, err := uuid()
	if err != nil {
		return "", err
	}
	return id[:16], nil
}
