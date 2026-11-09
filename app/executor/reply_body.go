package executor

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
)

// failedBody reads an error answer under the cap. The answer's body is spent
// afterwards, so the caller returns it again through replyWith.
func failedBody(resp Response) []byte {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, MaxErrorBody))
	_ = resp.Body.Close()
	return raw
}

// replyWith is resp with a new status and body. The headers that described the
// old body go, and the content type is set when one is given.
func replyWith(resp Response, status int, body []byte, contentType string) Response {
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
