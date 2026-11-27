package executor

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"strings"

	"github.com/andybalholm/brotli"

	"rllmx/app/platform/network"
)

// minCompressBody is where compression starts to pay for itself. A smaller body
// gains less than the encoder costs the gateway.
const minCompressBody = 1024

// compressBody encodes a request body when every guard holds. It returns the
// bytes to send and the Content-Encoding name, empty when the body goes as it
// is. A body that does not shrink is sent as it came: the provider would pay
// for the decode and gain nothing.
func compressBody(enc network.Compression, method string, body []byte, h http.Header) ([]byte, string) {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return body, ""
	}
	if len(body) < minCompressBody || h.Get("Content-Encoding") != "" ||
		!strings.HasPrefix(strings.ToLower(h.Get("Content-Type")), "application/json") {
		return body, ""
	}
	name := enc.Encoding()
	if name == "" {
		return body, ""
	}
	var buf bytes.Buffer
	switch enc {
	case network.CompressionGzip:
		w, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		if err != nil {
			return body, ""
		}
		_, _ = w.Write(body)
		_ = w.Close()
	case network.CompressionBrotli:
		// Level 1: a request body is compressed for the wire, not archived, and
		// the gateway pays the CPU on every call.
		w := brotli.NewWriterLevel(&buf, 1)
		_, _ = w.Write(body)
		_ = w.Close()
	}
	if buf.Len() >= len(body) {
		return body, ""
	}
	return buf.Bytes(), name
}
