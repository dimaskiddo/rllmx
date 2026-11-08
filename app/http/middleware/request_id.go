package middleware

import (
	"crypto/rand"
	"encoding/hex"

	contractshttp "github.com/goravel/framework/contracts/http"
)

const (
	requestIdHeader = "X-Request-Id"
	maxRequestIdLen = 64
)

type requestIdKey struct{}

// RequestId gives every request an id, answers it in X-Request-Id and makes it
// available to later middleware and handlers. A client id is kept only when it
// is short and made of letters, digits, dot, underscore and hyphen, because it
// lands in logs and response headers.
type RequestId struct{}

// Signature is how the stack names this middleware.
func (m *RequestId) Signature() string {
	return "RequestId"
}

// Handle accepts a caller's request id or makes one, then passes it on.
func (m *RequestId) Handle(ctx contractshttp.Context) {
	id := ctx.Request().Header(requestIdHeader)
	if !safeRequestId(id) {
		id = newRequestId()
	}
	ctx.WithValue(requestIdKey{}, id)
	ctx.Response().Header(requestIdHeader, id)
	ctx.Request().Next()
}

// RequestIdFrom returns the id RequestId gave the request, or "".
func RequestIdFrom(ctx contractshttp.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(requestIdKey{}).(string)
	return id
}

func newRequestId() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func safeRequestId(id string) bool {
	if id == "" || len(id) > maxRequestIdLen {
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
