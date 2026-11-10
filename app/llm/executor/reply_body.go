package executor

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	"rllmx/app/platform/file"
)

// FailedBody reads an error answer under the cap. The answer's body is spent
// afterwards, so the caller returns it again through replyWith.
func FailedBody(resp Response) []byte {
	raw := file.ReadUpTo(resp.Body, MaxErrorBody)
	_ = resp.Body.Close()
	return raw
}

// ReplyWith is resp with a new status and body. The headers that described the
// old body go, and the content type is set when one is given.
func ReplyWith(resp Response, status int, body []byte, contentType string) Response {
	h := resp.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Del("Content-Encoding")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return Response{Status: status, Header: h, Body: io.NopCloser(bytes.NewReader(body))}
}

// StreamHeader marks an answer as the stream it is, whatever it called itself.
func StreamHeader(h http.Header) http.Header {
	out := h.Clone()
	out.Set("Content-Type", "text/event-stream")
	return out
}
