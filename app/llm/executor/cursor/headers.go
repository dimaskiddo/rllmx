package cursor

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"rllmx/app/store/dialect"
)

// The identity this router presents. The version and commit are the service's
// own client, because the service refuses an unknown one; the operating system
// and architecture are the build target's, never the host's, so nothing about
// the machine leaves it.
const (
	clientVersion = "3.12.17"
	clientCommit  = "0fb762053c34788bb7760d5673f8a6d4c8589d50"
	clientType    = "ide"
	clientAgent   = "connect-es/1.6.1"
	deviceType    = "desktop"
	// sessionNamespace is the fixed UUID namespace the session id is derived in,
	// so one token keeps one session across restarts.
	sessionNamespace = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
)

// headers are what a run request carries.
func headers(token, machineID string, ghost bool) map[string]string {
	h := map[string]string{
		"Authorization":               "Bearer " + bareToken(token),
		"Accept":                      "application/connect+proto",
		"User-Agent":                  clientAgent,
		"connect-protocol-version":    "1",
		"connect-accept-encoding":     "gzip",
		"x-client-key":                hexHash(bareToken(token)),
		"x-cursor-checksum":           checksum(machineID, time.Now()),
		"x-cursor-client-version":     clientVersion,
		"x-cursor-client-commit":      clientCommit,
		"x-cursor-client-type":        clientType,
		"x-cursor-client-os":          clientOS(),
		"x-cursor-client-arch":        clientArch(),
		"x-cursor-client-device-type": deviceType,
		"x-cursor-config-version":     dialect.NewID(),
		"x-cursor-timezone":           "UTC",
		"x-ghost-mode":                ghostWord(ghost),
		"x-request-id":                dialect.NewID(),
		"x-amzn-trace-id":             "Root=" + dialect.NewID(),
		"x-session-id":                sessionID(bareToken(token)),
	}
	return h
}

// bareToken drops the vendor's own prefix from a token that carries one.
func bareToken(token string) string {
	if i := strings.LastIndex(token, "::"); i >= 0 {
		return token[i+2:]
	}
	return token
}

// ghostWord is the header's own spelling of a bool.
func ghostWord(on bool) string {
	if on {
		return "true"
	}
	return "false"
}

// hexHash is the token's own key: sha256 in hex.
func hexHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sessionID names one conversation with the service: a UUIDv5 of the token, so
// the same account keeps the same session and no request carries a value a
// client chose.
func sessionID(token string) string {
	ns := uuid.MustParse(sessionNamespace)
	return uuid.NewSHA1(ns, []byte(token)).String()
}

// checksum is the obfuscated timestamp the service wants beside the machine id:
// six bytes of microseconds, XORed with a rolling key, in URL-safe base64 with
// no padding, then the id. It is not a signature; it tells the service the
// request came from a client that knows the format.
func checksum(machineID string, now time.Time) string {
	stamp := uint64(now.Unix())
	var b [6]byte
	for i := range b {
		b[i] = byte(stamp >> (8 * (5 - i)))
	}
	var key byte = 165
	for i := range b {
		b[i] = (b[i] ^ key) + byte(i)
		key = b[i]
	}
	return base64.RawURLEncoding.EncodeToString(b[:]) + machineID
}

// clientOS and clientArch name the build target, not the machine the router
// happens to run on.
func clientOS() string {
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	}
	return "linux"
}

func clientArch() string {
	if runtime.GOARCH == "arm64" {
		return "aarch64"
	}
	return runtime.GOARCH
}
