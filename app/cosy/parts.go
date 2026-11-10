package cosy

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// sigPathOf is the path the digest covers: the address's own, without the
// gateway's "/algo" prefix.
func sigPathOf(path string) string {
	return strings.TrimPrefix(path, "/algo")
}

// timestampNow is the signing instant in whole seconds.
func timestampNow() string { return strconv.FormatInt(time.Now().Unix(), 10) }

func itoa(n int) string { return strconv.Itoa(n) }

// uuid is a version-4 identifier, made here rather than derived from the host.
func uuid() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

// jsonIdentity writes the identity in the field order the gateway encrypted, so
// the bytes it decrypts are the bytes it expects.
func jsonIdentity(id Identity) string {
	return `{"uid":` + quote(id.UserID) + `,"security_oauth_token":` + quote(id.Token) +
		`,"name":` + quote(id.Name) + `,"aid":"","email":` + quote(id.Email) + `}`
}

// jsonPayload writes the envelope the digest covers.
func jsonPayload(info, cosyKey, requestID, version string) ([]byte, error) {
	_ = cosyKey
	out := `{"version":"v1","requestId":` + quote(requestID) + `,"info":` + quote(info) +
		`,"cosyVersion":` + quote(version) + `,"ideVersion":""}`
	return []byte(out), nil
}

// quote writes a string as a JSON literal.
func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte("0123456789abcdef"[r>>4])
				b.WriteByte("0123456789abcdef"[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// bytesOf is n copies of b.
func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
