package cosy

import (
	"bytes"
	"encoding/json"
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

// jsonIdentity writes the identity in the field order the gateway encrypted, so
// the bytes it decrypts are the bytes it expects.
func jsonIdentity(id Identity) string {
	return `{"uid":` + quote(id.UserID) + `,"security_oauth_token":` + quote(id.Token) +
		`,"name":` + quote(id.Name) + `,"aid":"","email":` + quote(id.Email) + `}`
}

// jsonPayload writes the envelope the digest covers.
func jsonPayload(info, requestID, version string) []byte {
	return []byte(`{"version":"v1","requestId":` + quote(requestID) + `,"info":` + quote(info) +
		`,"cosyVersion":` + quote(version) + `,"ideVersion":""}`)
}

// quote writes a string as a JSON literal, without escaping HTML characters: the
// gateway verifies a digest over these exact bytes.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// bytesOf is n copies of b.
func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
