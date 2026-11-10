package middleware

import (
	"crypto/rand"
	"encoding/hex"

	contractshttp "github.com/goravel/framework/contracts/http"
)

const (
	requestIDHeader = "X-Request-Id"
	maxRequestIDLen = 64
)

type requestIDKey struct{}

// RequestID gives every request an id, answers it in X-Request-Id and makes it
// available to later middleware and handlers. A client id is kept only when it
// is short and made of letters, digits, dot, underscore and hyphen, because it
// lands in logs and response headers.
type RequestID struct{}

// Signature is how the stack names this middleware.
func (m *RequestID) Signature() string {
	return "RequestID"
}

// Handle accepts a caller's request id or makes one, then passes it on.
func (m *RequestID) Handle(ctx contractshttp.Context) {
	id := ctx.Request().Header(requestIDHeader)
	if !safeRequestID(id) {
		id = newRequestID()
	}
	ctx.WithValue(requestIDKey{}, id)
	ctx.Response().Header(requestIDHeader, id)
	ctx.Request().Next()
}

// RequestIDFrom returns the id RequestID gave the request, or "".
func RequestIDFrom(ctx contractshttp.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func safeRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}
