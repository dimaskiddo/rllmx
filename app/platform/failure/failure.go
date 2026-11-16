// Package failure writes the error bodies clients expect: the OpenAI,
// Anthropic, Responses, Gemini and Ollama shapes on the client API and a flat
// body on the dashboard API. The same table feeds the frames sent after a
// stream began.
package failure

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rllmx/app/platform/text"
)

// Format is the error shape a route answers in.
type Format int

const (
	// Flat is {"error":"message"}, used by the dashboard API and static routes.
	Flat Format = iota
	// OpenAI is the OpenAI error object.
	OpenAI
	// Anthropic is the Anthropic error envelope.
	Anthropic
	// Gemini is the Google error object.
	Gemini
	// Responses carries the OpenAI body; only the stream frame differs.
	Responses
	// Ollama is {"error":"message"} with the real status.
	Ollama
)

const maxMessageBytes = 2000

// ForPath picks the error format a request path expects.
func ForPath(path string) Format {
	switch {
	case path == "/v1/responses" || strings.HasPrefix(path, "/v1/responses/"):
		return Responses
	case strings.HasPrefix(path, "/v1/api/"):
		return Ollama
	case path == "/v1/messages" || strings.HasPrefix(path, "/v1/messages/"):
		return Anthropic
	case path == "/v1beta" || strings.HasPrefix(path, "/v1beta/"):
		return Gemini
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return OpenAI
	}
	return Flat
}

// ForClient picks the format from a client format name; an unknown name gets
// the OpenAI shape.
func ForClient(clientFormat string) Format {
	switch clientFormat {
	case "claude":
		return Anthropic
	case "gemini":
		return Gemini
	case "openai-responses":
		return Responses
	case "ollama":
		return Ollama
	}
	return OpenAI
}

type entry struct {
	openAIType, openAICode, anthropicType, geminiStatus, message string
}

var table = map[int]entry{
	400: {"invalid_request_error", "bad_request", "invalid_request_error", "INVALID_ARGUMENT", "Bad request"},
	401: {"authentication_error", "invalid_api_key", "authentication_error", "UNAUTHENTICATED", "Invalid API key provided"},
	402: {"billing_error", "payment_required", "billing_error", "FAILED_PRECONDITION", "Payment required"},
	403: {"permission_error", "insufficient_quota", "permission_error", "PERMISSION_DENIED", "You exceeded your current quota"},
	404: {"invalid_request_error", "model_not_found", "not_found_error", "NOT_FOUND", "Model not found"},
	405: {"invalid_request_error", "method_not_allowed", "invalid_request_error", "INVALID_ARGUMENT", "Method not allowed"},
	406: {"invalid_request_error", "model_not_supported", "invalid_request_error", "INVALID_ARGUMENT", "Model not supported"},
	408: {"invalid_request_error", "request_timeout", "invalid_request_error", "DEADLINE_EXCEEDED", "Request timed out while reading the body"},
	413: {"invalid_request_error", "request_too_large", "request_too_large", "INVALID_ARGUMENT", "Request body too large"},
	429: {"rate_limit_error", "rate_limit_exceeded", "rate_limit_error", "RESOURCE_EXHAUSTED", "Rate limit exceeded"},
	500: {"server_error", "internal_server_error", "api_error", "INTERNAL", "Internal server error"},
	502: {"server_error", "bad_gateway", "api_error", "UNAVAILABLE", "Bad gateway - upstream provider error"},
	503: {"server_error", "service_unavailable", "overloaded_error", "UNAVAILABLE", "Service temporarily unavailable"},
	504: {"server_error", "gateway_timeout", "api_error", "DEADLINE_EXCEEDED", "Gateway timeout"},
	529: {"server_error", "service_unavailable", "overloaded_error", "UNAVAILABLE", "Service overloaded"},
}

func lookup(status int) entry {
	if e, ok := table[status]; ok {
		return e
	}
	if status >= 500 {
		return entry{"server_error", "internal_server_error", "api_error", "INTERNAL", ""}
	}
	return entry{"invalid_request_error", "", "invalid_request_error", "INVALID_ARGUMENT", ""}
}

var flatDefaults = map[int]string{
	400: "Bad request", 401: "Unauthorized", 403: "Forbidden", 404: "Not found", 405: "Method not allowed",
	408: "Request timed out", 413: "Request body too large", 429: "Too many requests", 500: "Internal server error",
	502: "Bad gateway", 503: "Service unavailable", 504: "Gateway timeout",
}

func bounded(msg string) string {
	return strings.ToValidUTF8(text.CutBytes(msg, maxMessageBytes), "")
}

func message(f Format, status int, msg string, e entry) string {
	if msg = bounded(msg); msg != "" {
		return msg
	}
	if f == Flat {
		if m, ok := flatDefaults[status]; ok {
			return m
		}
		return "An error occurred"
	}
	if e.message != "" {
		return e.message
	}
	return "An error occurred"
}

// marshal escapes HTML so a message that echoes input cannot become markup.
func marshal(v any) []byte {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

type openAIBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

type anthropicBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type geminiBody struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// Body renders an error in the given format.
func Body(f Format, status int, msg string) []byte {
	e := lookup(status)
	m := message(f, status, msg, e)
	switch f {
	case OpenAI, Responses:
		var b openAIBody
		b.Error.Message, b.Error.Type, b.Error.Code = m, e.openAIType, e.openAICode
		return marshal(b)
	case Anthropic:
		var b anthropicBody
		b.Type = "error"
		b.Error.Type, b.Error.Message = e.anthropicType, m
		return marshal(b)
	case Gemini:
		var b geminiBody
		b.Error.Code, b.Error.Message, b.Error.Status = status, m, e.geminiStatus
		return marshal(b)
	}
	return marshal(map[string]string{"error": m})
}

// ReadStatus maps a failed request body read to its status: 413 over the
// limit, 408 on a read timeout, else 400.
func ReadStatus(err error) int {
	var tooBig *http.MaxBytesError
	var netErr net.Error
	switch {
	case errors.As(err, &tooBig):
		return http.StatusRequestEntityTooLarge
	case errors.As(err, &netErr) && netErr.Timeout():
		return http.StatusRequestTimeout
	}
	return http.StatusBadRequest
}

// Headers are the response headers every error body carries.
func Headers() http.Header {
	h := http.Header{}
	jsonHeaders(h)
	return h
}

func jsonHeaders(h http.Header) {
	h.Set("Content-Type", "application/json")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
}

// NotAllowed names the model a key may not call. The name is the client's own
// string, so repeating it reveals nothing internal.
func NotAllowed(model string) string {
	return "This API key is not allowed to use model \"" + model + "\""
}

// Write sends an error response in the given format.
func Write(w http.ResponseWriter, f Format, status int, msg string) {
	WriteWith(w, f, status, msg, nil)
}

// WriteWith is Write with extra response headers.
func WriteWith(w http.ResponseWriter, f Format, status int, msg string, extra http.Header) {
	writeJSON(w, status, Body(f, status, msg), extra)
}

func writeJSON(w http.ResponseWriter, status int, body []byte, extra http.Header) {
	h := w.Header()
	for k, v := range extra {
		h[http.CanonicalHeaderKey(k)] = v
	}
	jsonHeaders(h)
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Unavailable is the body and extra headers of the 503 sent when every account
// is locked. Retry-After is whole seconds, at least 1, and the same moment is
// added to the body as retryAfter, so a combo that reads the body finds it too.
// extra carries the upstream's own hints, such as rate-limit counters; the
// router's Retry-After replaces any it holds.
func Unavailable(f Format, msg string, retryAt, now time.Time, extra http.Header) ([]byte, http.Header) {
	secs := max(int64((retryAt.Sub(now)+time.Second-1)/time.Second), 1)
	body := Body(f, http.StatusServiceUnavailable, msg)
	if out, ok := withRetryAfter(body, retryAt.UTC().Format(time.RFC3339)); ok {
		body = out
	}
	h := extra.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Retry-After", strconv.FormatInt(secs, 10))
	return body, h
}

func withRetryAfter(body []byte, at string) ([]byte, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil, false
	}
	raw, _ := json.Marshal(at)
	m["retryAfter"] = raw
	return marshal(m), true
}

// WriteFor picks the format from the request path and sends no body for HEAD.
func WriteFor(w http.ResponseWriter, r *http.Request, status int, msg string) {
	if r.Method == http.MethodHead {
		jsonHeaders(w.Header())
		w.WriteHeader(status)
		return
	}
	Write(w, ForPath(r.URL.Path), status, msg)
}

// Frame is the error to send on a stream whose 200 was already written. It is
// empty for Flat. Payloads are single-line JSON, so a message cannot add frames.
func Frame(f Format, status int, msg string) []byte {
	payload := Body(f, status, msg)
	switch f {
	case OpenAI:
		return []byte("data: " + string(payload) + "\n\ndata: [DONE]\n\n")
	case Anthropic:
		return []byte("event: error\ndata: " + string(payload) + "\n\n")
	case Gemini:
		return []byte("data: " + string(payload) + "\r\n\r\n")
	case Responses:
		return []byte("event: response.failed\ndata: " + string(payload) + "\n\n")
	case Ollama:
		return append(append([]byte(nil), payload...), '\n')
	}
	return nil
}

// ClassOf is the short code a usage row carries for a status. Both gateways use
// it, so one failure never reads differently in the stored rows by the route it
// took.
func ClassOf(status int) string {
	switch {
	case status < 400:
		return "none"
	case status == http.StatusUnauthorized:
		return "auth"
	case status == http.StatusForbidden:
		return "access_denied"
	case status == http.StatusPaymentRequired:
		return "quota"
	case status == http.StatusTooManyRequests:
		return "rate_limit"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusGatewayTimeout:
		return "timeout"
	case status >= 500:
		return "upstream_5xx"
	}
	return "invalid_request"
}

// ClientStatus is the status a client sees for an upstream status. A refused
// credential is the router's problem, not the client's: a 401 or 403 would make
// the client discard a good key.
func ClientStatus(status int) int {
	if status < 400 || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return http.StatusBadGateway
	}
	return status
}
