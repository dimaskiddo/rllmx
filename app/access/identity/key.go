package identity

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// DefaultAPIKeySecret is the checksum secret a deployment gets without setting
// AUTH_API_KEY_SECRET. It is the reference's own public default, kept so a
// database migrated from it keeps validating; a start-up warning says so.
const DefaultAPIKeySecret = "endpoint-proxy-api-key-secret"

const (
	keyPrefix   = "sk-"
	keyIDLen    = 6
	crcLen      = 8
	minKeyBytes = 8
	maxKeyBytes = 256
	// keyIDChars is the alphabet a new key id draws from. It is the reference's,
	// so a generated key looks like one it would have produced.
	keyIDChars = "abcdefghijklmnopqrstuvwxyz0123456789"
)

// ErrKeyRandom means the system random source failed. It never happens on a
// healthy host and must never be worked around with a weaker source.
var ErrKeyRandom = errors.New("identity: the system random source failed")

// Class says what shape a presented key has. The checksum is informational: a
// key is valid only when an active row matches it, and the reference
// authenticates by exact match too.
type Class int

const (
	// ClassMalformed is a key no version of this product wrote.
	ClassMalformed Class = iota
	// ClassLegacy is the old `sk-{random}` shape, with no machine id and no checksum.
	ClassLegacy
	// ClassChecksumMismatch is the `sk-{machineId}-{keyId}-{crc8}` shape whose
	// checksum does not compute, usually because the secret changed.
	ClassChecksumMismatch
	// ClassOK is the current shape with a matching checksum.
	ClassOK
)

// String names the class the way keys:list prints it.
func (c Class) String() string {
	switch c {
	case ClassOK:
		return "ok"
	case ClassChecksumMismatch:
		return "mismatch"
	case ClassLegacy:
		return "legacy"
	}
	return "malformed"
}

// Parsed is a key taken apart.
type Parsed struct {
	MachineID string
	KeyID     string
	Class     Class
}

// Keys builds, parses and classifies client keys under one checksum secret.
type Keys struct {
	secret []byte
}

// NewKeys builds the key codec. An empty secret uses the public default.
func NewKeys(secret string) Keys {
	if secret == "" {
		secret = DefaultAPIKeySecret
	}
	return Keys{secret: []byte(secret)}
}

// DefaultSecret reports whether the codec runs on the public default, which the
// start-up warns about.
func (k Keys) DefaultSecret() bool {
	return string(k.secret) == DefaultAPIKeySecret
}

// New returns a key for machineID with a fresh random key id.
func (k Keys) New(machineID string) (string, error) {
	id, err := randomKeyID()
	if err != nil {
		return "", err
	}
	return keyPrefix + machineID + "-" + id + "-" + k.crc(machineID, id), nil
}

// Parse takes a key apart. A key no version of this product wrote is
// ClassMalformed with no parts.
func (k Keys) Parse(key string) Parsed {
	if !wellFormed(key) {
		return Parsed{Class: ClassMalformed}
	}
	parts := strings.Split(key, "-")
	if len(parts) != 4 {
		return Parsed{Class: ClassLegacy}
	}
	p := Parsed{MachineID: parts[1], KeyID: parts[2], Class: ClassChecksumMismatch}
	if p.MachineID == "" || p.KeyID == "" || len(parts[3]) != crcLen {
		return p
	}
	if hmac.Equal([]byte(k.crc(p.MachineID, p.KeyID)), []byte(parts[3])) {
		p.Class = ClassOK
	}
	return p
}

// Classify returns only the class of a key.
func (k Keys) Classify(key string) Class { return k.Parse(key).Class }

// crc is the first eight hex characters of the keyed hash over the machine id
// and key id. The name is the reference's; the construction is an HMAC.
func (k Keys) crc(machineID, keyID string) string {
	mac := hmac.New(sha256.New, k.secret)
	mac.Write([]byte(machineID + keyID))
	return hex.EncodeToString(mac.Sum(nil))[:crcLen]
}

// wellFormed applies the shape rules a key must meet before any lookup, so a
// malformed key never costs a query: the prefix, the alphabet, and the length
// bounds.
func wellFormed(key string) bool {
	if len(key) < minKeyBytes || len(key) > maxKeyBytes || !strings.HasPrefix(key, keyPrefix) {
		return false
	}
	for i := 0; i < len(key); i++ {
		switch c := key[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

// randomKeyID draws six characters from keyIDChars, rejecting a byte that would
// bias the alphabet rather than folding it with a modulo.
func randomKeyID() (string, error) {
	const limit = 256 / len(keyIDChars) * len(keyIDChars)
	out := make([]byte, 0, keyIDLen)
	buf := make([]byte, 1)
	for len(out) < keyIDLen {
		if _, err := rand.Read(buf); err != nil {
			return "", ErrKeyRandom
		}
		if int(buf[0]) >= limit {
			continue
		}
		out = append(out, keyIDChars[int(buf[0])%len(keyIDChars)])
	}
	return string(out), nil
}
