// Package sealer seals the secrets kept in the database with AES-256-GCM. It is
// a format owner: a stored value is "enc:v1:" and unpadded base64url of
// nonce(12) || ciphertext+tag, and changing any of it breaks every sealed row.
// Goravel's Crypt facade writes a different format and is not installed.
package sealer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Prefix marks a sealed value.
const Prefix = "enc:v1:"

// SecretBytes is the required secret length.
const SecretBytes = 32

// The HKDF labels keep the "rllm" name they were first written with: they are
// part of the stored format, and a sealed database must open under the same
// secret whichever program wrote it.
const (
	infoKey   = "rllm/v1/aes-gcm"
	infoNonce = "rllm/v1/nonce"
)

var (
	// ErrNotSealed means the text has no sealed prefix.
	ErrNotSealed = errors.New("sealer: value is not sealed")
	// ErrTampered means the text is sealed but does not open under this secret
	// and place: the secret is wrong, the value was changed, or it was moved.
	ErrTampered = errors.New("sealer: sealed value does not open")
)

var b64 = base64.RawURLEncoding

// Box seals and opens values under one secret.
type Box struct {
	aead     cipher.AEAD
	nonceKey []byte
}

// New returns a Box for a 32-byte secret.
func New(secret []byte) (*Box, error) {
	if len(secret) != SecretBytes {
		return nil, fmt.Errorf("sealer: the secret must be %d bytes", SecretBytes)
	}
	key, err := hkdf.Key(sha256.New, secret, nil, infoKey, 32)
	if err != nil {
		return nil, fmt.Errorf("sealer: derive key: %w", err)
	}
	nonceKey, err := hkdf.Key(sha256.New, secret, nil, infoNonce, 32)
	if err != nil {
		return nil, fmt.Errorf("sealer: derive nonce key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("sealer: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("sealer: %w", err)
	}
	return &Box{aead: aead, nonceKey: nonceKey}, nil
}

// IsSealed reports whether s carries the sealed prefix.
func IsSealed(s string) bool { return strings.HasPrefix(s, Prefix) }

func (b *Box) seal(nonce []byte, aad, plain string) string {
	out := b.aead.Seal(append([]byte(nil), nonce...), nonce, []byte(plain), []byte(aad))
	return Prefix + b64.EncodeToString(out)
}

// Seal encrypts plain with a random nonce. Equal inputs give different outputs.
// It panics when the system has no randomness, because sealing with a guessable
// nonce would be worse than stopping.
func (b *Box) Seal(aad, plain string) string {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic("sealer: no randomness: " + err.Error())
	}
	return b.seal(nonce, aad, plain)
}

// SealFixed encrypts plain with a nonce derived from the place and the value
// (a synthetic IV), so equal inputs give equal outputs. Use it only where the
// stored value must be found by equality; it reveals whether two values match.
func (b *Box) SealFixed(aad, plain string) string {
	m := hmac.New(sha256.New, b.nonceKey)
	m.Write([]byte(aad))
	m.Write([]byte{0})
	m.Write([]byte(plain))
	return b.seal(m.Sum(nil)[:b.aead.NonceSize()], aad, plain)
}

// Open decrypts a sealed value that belongs to the place named by aad.
func (b *Box) Open(aad, sealed string) (string, error) {
	rest, ok := strings.CutPrefix(sealed, Prefix)
	if !ok {
		return "", ErrNotSealed
	}
	raw, err := b64.DecodeString(rest)
	n := b.aead.NonceSize()
	if err != nil || len(raw) < n+b.aead.Overhead() {
		return "", ErrTampered
	}
	plain, err := b.aead.Open(nil, raw[:n], raw[n:], []byte(aad))
	if err != nil {
		return "", ErrTampered
	}
	return string(plain), nil
}
