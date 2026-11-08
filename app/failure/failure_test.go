package failure

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"rllmx/tests"
)

func decode(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body is not JSON: %q (%v)", body, err)
	}
	return m
}

func TestFormatForPath(t *testing.T) {
	cases := map[string]Format{
		"/v1/messages":                          Anthropic,
		"/v1/messages/count_tokens":             Anthropic,
		"/v1/chat/completions":                  OpenAI,
		"/v1/responses":                         Responses,
		"/v1/responses/compact":                 Responses,
		"/v1/api/chat":                          Ollama,
		"/v1/models":                            OpenAI,
		"/v1/embeddings":                        OpenAI,
		"/v1/systemone":                         OpenAI,
		"/v1beta/models/gemini:generateContent": Gemini,
		"/v1beta/models":                        Gemini,
		"/api/health":                           Flat,
		"/api/anything":                         Flat,
		"/dashboard":                            Flat,
		"/":                                     Flat,
		"/v1messages":                           Flat,
		"/v1/messagesx":                         OpenAI,
		"/v1betax/models":                       Flat,
	}
	for path, want := range cases {
		if got := ForPath(path); got != want {
			t.Errorf("ForPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestOpenAIShapePerStatus(t *testing.T) {
	want := map[int][3]string{
		400: {"invalid_request_error", "bad_request", "Bad request"},
		401: {"authentication_error", "invalid_api_key", "Invalid API key provided"},
		402: {"billing_error", "payment_required", "Payment required"},
		403: {"permission_error", "insufficient_quota", "You exceeded your current quota"},
		404: {"invalid_request_error", "model_not_found", "Model not found"},
		405: {"invalid_request_error", "method_not_allowed", "Method not allowed"},
		406: {"invalid_request_error", "model_not_supported", "Model not supported"},
		429: {"rate_limit_error", "rate_limit_exceeded", "Rate limit exceeded"},
		500: {"server_error", "internal_server_error", "Internal server error"},
		502: {"server_error", "bad_gateway", "Bad gateway - upstream provider error"},
		503: {"server_error", "service_unavailable", "Service temporarily unavailable"},
		504: {"server_error", "gateway_timeout", "Gateway timeout"},
	}
	for status, w := range want {
		rec := httptest.NewRecorder()
		Write(rec, OpenAI, status, "")
		if rec.Code != status || rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%d: code %d type %q", status, rec.Code, rec.Header().Get("Content-Type"))
		}
		e := decode(t, rec.Body.String())["error"].(map[string]any)
		if e["type"] != w[0] || e["code"] != w[1] || e["message"] != w[2] {
			t.Errorf("%d: got %v, want %v", status, e, w)
		}
	}
}

func TestOpenAIUnmappedStatuses(t *testing.T) {
	for status, wantType := range map[int]string{418: "invalid_request_error", 599: "server_error", 507: "server_error"} {
		rec := httptest.NewRecorder()
		Write(rec, OpenAI, status, "custom")
		e := decode(t, rec.Body.String())["error"].(map[string]any)
		if e["type"] != wantType || e["message"] != "custom" {
			t.Errorf("%d: %v", status, e)
		}
	}
	rec := httptest.NewRecorder()
	Write(rec, OpenAI, 418, "")
	if m := decode(t, rec.Body.String())["error"].(map[string]any)["message"]; m != "An error occurred" {
		t.Errorf("empty message on an unmapped status = %q", m)
	}
}

func TestRouterOwnStatusesHaveSpecificCodes(t *testing.T) {
	cases := map[int]string{408: "request_timeout", 413: "request_too_large"}
	for status, code := range cases {
		rec := httptest.NewRecorder()
		Write(rec, OpenAI, status, "")
		e := decode(t, rec.Body.String())["error"].(map[string]any)
		if e["code"] != code || e["type"] != "invalid_request_error" || e["message"] == "" {
			t.Errorf("%d: %v", status, e)
		}
	}
}

func TestAnthropicShape(t *testing.T) {
	want := map[int]string{400: "invalid_request_error", 401: "authentication_error", 403: "permission_error", 404: "not_found_error",
		405: "invalid_request_error", 408: "invalid_request_error", 413: "request_too_large", 429: "rate_limit_error", 500: "api_error",
		502: "api_error", 503: "overloaded_error", 504: "api_error", 529: "overloaded_error"}
	for status, typ := range want {
		rec := httptest.NewRecorder()
		Write(rec, Anthropic, status, "boom")
		m := decode(t, rec.Body.String())
		e, _ := m["error"].(map[string]any)
		if m["type"] != "error" || e["type"] != typ || e["message"] != "boom" {
			t.Errorf("%d: %v", status, m)
		}
		if _, has := e["code"]; has {
			t.Errorf("%d: Anthropic errors carry no code: %v", status, e)
		}
	}
}

func TestGeminiShape(t *testing.T) {
	want := map[int]string{400: "INVALID_ARGUMENT", 401: "UNAUTHENTICATED", 403: "PERMISSION_DENIED", 404: "NOT_FOUND", 405: "INVALID_ARGUMENT",
		408: "DEADLINE_EXCEEDED", 413: "INVALID_ARGUMENT", 429: "RESOURCE_EXHAUSTED", 500: "INTERNAL", 502: "UNAVAILABLE", 503: "UNAVAILABLE",
		504: "DEADLINE_EXCEEDED"}
	for status, st := range want {
		rec := httptest.NewRecorder()
		Write(rec, Gemini, status, "boom")
		e := decode(t, rec.Body.String())["error"].(map[string]any)
		if int(e["code"].(float64)) != status || e["status"] != st || e["message"] != "boom" {
			t.Errorf("%d: %v", status, e)
		}
	}
}

func TestFlatShape(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, Flat, 401, "")
	m := decode(t, rec.Body.String())
	if m["error"] != "Unauthorized" || len(m) != 1 {
		t.Fatalf("flat 401 = %v", m)
	}
	rec = httptest.NewRecorder()
	Write(rec, Flat, 403, "Local only")
	if decode(t, rec.Body.String())["error"] != "Local only" {
		t.Fatal("custom flat message lost")
	}
}

func TestDefaultMessagesAreNeverEmpty(t *testing.T) {
	for _, f := range []Format{OpenAI, Anthropic, Gemini, Flat} {
		for _, status := range []int{400, 401, 403, 404, 405, 408, 413, 429, 500, 502, 503, 504} {
			rec := httptest.NewRecorder()
			Write(rec, f, status, "")
			if strings.Contains(rec.Body.String(), `"message":""`) || strings.Contains(rec.Body.String(), `"error":""`) {
				t.Errorf("format %v status %d has an empty message: %s", f, status, rec.Body.String())
			}
		}
	}
}

func TestMessagesAreEscapedAndBounded(t *testing.T) {
	evil := "</script><img src=x onerror=1> \"\\\x00\n" + strings.Repeat("a", 5000)
	for _, f := range []Format{OpenAI, Anthropic, Gemini, Flat} {
		rec := httptest.NewRecorder()
		Write(rec, f, 400, evil)
		decode(t, rec.Body.String())
		if strings.Contains(rec.Body.String(), "</script>") {
			t.Errorf("format %v does not escape HTML in the message", f)
		}
		if rec.Body.Len() > 2500 {
			t.Errorf("format %v body is %d bytes: messages must be bounded", f, rec.Body.Len())
		}
	}
}

func TestHeadersAndExtras(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteWith(rec, OpenAI, 429, "slow down", http.Header{"Retry-After": {"7"}, "X-Extra": {"1"}})
	if rec.Header().Get("Retry-After") != "7" || rec.Header().Get("X-Extra") != "1" {
		t.Fatalf("extra headers lost: %v", rec.Header())
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatal("content type")
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff missing on an error body")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("errors must not be cached")
	}
}

func TestWriteOnABodylessMethod(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteFor(rec, httptest.NewRequest(http.MethodHead, "/api/x", nil), 401, "")
	if rec.Code != 401 || rec.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteFor(rec, httptest.NewRequest(http.MethodGet, "/v1/messages", nil), 401, "")
	if m := decode(t, rec.Body.String()); m["type"] != "error" {
		t.Fatalf("WriteFor must pick the format from the path: %v", m)
	}
}

func TestStreamFrames(t *testing.T) {
	o := string(Frame(OpenAI, 504, "stalled"))
	if !strings.HasPrefix(o, "data: ") || !strings.HasSuffix(o, "\n\ndata: [DONE]\n\n") {
		t.Fatalf("OpenAI frame = %q", o)
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(strings.SplitN(o, "\n\n", 2)[0], "data: "), "\n")
	e := decode(t, payload)["error"].(map[string]any)
	if e["type"] != "server_error" || e["code"] != "gateway_timeout" || e["message"] != "stalled" {
		t.Fatalf("OpenAI frame payload = %v", e)
	}

	a := string(Frame(Anthropic, 529, "busy"))
	if !strings.HasPrefix(a, "event: error\ndata: ") || !strings.HasSuffix(a, "\n\n") || strings.Contains(a, "[DONE]") {
		t.Fatalf("Anthropic frame = %q", a)
	}
	m := decode(t, strings.TrimSuffix(strings.TrimPrefix(a, "event: error\ndata: "), "\n\n"))
	if m["type"] != "error" || m["error"].(map[string]any)["type"] != "overloaded_error" {
		t.Fatalf("Anthropic frame payload = %v", m)
	}

	g := string(Frame(Gemini, 503, "down"))
	if !strings.HasPrefix(g, "data: ") || strings.Contains(g, "[DONE]") || !strings.HasSuffix(g, "\r\n\r\n") {
		t.Fatalf("Gemini frame = %q", g)
	}
	if strings.ContainsAny(strings.TrimSuffix(g, "\r\n\r\n"), "\r\n") {
		t.Fatalf("Gemini frame payload spans lines: %q", g)
	}
	if string(Frame(Flat, 500, "x")) != "" {
		t.Fatal("a flat format has no stream frame")
	}
}

func TestFramePayloadsNeverContainRawNewlines(t *testing.T) {
	for _, f := range []Format{OpenAI, Anthropic, Gemini} {
		fr := string(Frame(f, 500, "line1\nline2\r\nline3"))
		terminator := "\n\n"
		if f == Gemini {
			terminator = "\r\n\r\n"
		}
		first := fr[:strings.Index(fr, terminator)]
		for line := range strings.SplitSeq(first, "\n") {
			if line != "" && !strings.HasPrefix(line, "data: ") && !strings.HasPrefix(line, "event: ") {
				t.Errorf("format %v: a message newline escaped into the framing: %q", f, fr)
			}
		}
		if strings.Count(first, "data: ") != 1 {
			t.Errorf("format %v: the message produced a second data line: %q", f, fr)
		}
	}
}

func TestInvalidUTF8InAMessageStillYieldsValidJSON(t *testing.T) {
	for _, f := range []Format{OpenAI, Anthropic, Gemini, Flat} {
		rec := httptest.NewRecorder()
		Write(rec, f, 400, "ok\xff\xfe\xc0bad")
		decode(t, rec.Body.String())
		if strings.Contains(rec.Body.String(), "�") {
			t.Errorf("format %v: replacement characters kept in %q", f, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "okbad") {
			t.Errorf("format %v: the valid text was lost: %q", f, rec.Body.String())
		}
	}
}

func TestContentLengthMatchesTheBody(t *testing.T) {
	for _, f := range []Format{OpenAI, Anthropic, Gemini, Flat} {
		rec := httptest.NewRecorder()
		Write(rec, f, 429, "héllo wörld")
		n, err := strconv.Atoi(rec.Header().Get("Content-Length"))
		if err != nil || n != rec.Body.Len() {
			t.Errorf("format %v: Content-Length %q, body %d bytes", f, rec.Header().Get("Content-Length"), rec.Body.Len())
		}
	}
}

func TestEveryErrorResponseIsMarkedNoSniff(t *testing.T) {
	for _, f := range []Format{OpenAI, Anthropic, Gemini, Flat} {
		rec := httptest.NewRecorder()
		Write(rec, f, 500, "")
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("format %v: nosniff missing", f)
		}
	}
	rec := httptest.NewRecorder()
	WriteFor(rec, httptest.NewRequest(http.MethodHead, "/api/x", nil), 401, "")
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("HEAD error responses lost their headers")
	}
}

func TestWriteUnavailableCarriesRetryAfterInHeaderAndBody(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		at    time.Time
		wantS string
	}{
		{"seconds round up", now.Add(90*time.Second + 200*time.Millisecond), "91"},
		{"at least one second", now.Add(10 * time.Millisecond), "1"},
		{"already past", now.Add(-time.Minute), "1"},
	} {
		rec := httptest.NewRecorder()
		WriteUnavailable(rec, OpenAI, "[openai/gpt] all locked (reset after 1m)", c.at, now, nil)
		if rec.Code != 503 || rec.Header().Get("Retry-After") != c.wantS {
			t.Errorf("%s: code=%d retry-after=%q", c.name, rec.Code, rec.Header().Get("Retry-After"))
		}
		m := decode(t, rec.Body.String())
		e, _ := m["error"].(map[string]any)
		if e["type"] != "server_error" || e["code"] != "service_unavailable" || e["message"] != "[openai/gpt] all locked (reset after 1m)" {
			t.Errorf("%s: error object %v", c.name, e)
		}
		if m["retryAfter"] != c.at.UTC().Format(time.RFC3339) {
			t.Errorf("%s: retryAfter %v", c.name, m["retryAfter"])
		}
	}
}

func TestWriteUnavailableOtherFormatsKeepTheirShape(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	WriteUnavailable(rec, Anthropic, "busy", now.Add(time.Minute), now, nil)
	m := decode(t, rec.Body.String())
	if m["type"] != "error" || m["retryAfter"] == nil || rec.Header().Get("Retry-After") != "60" {
		t.Fatalf("got %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	WriteUnavailable(rec, Flat, "busy", now.Add(time.Minute), now, nil)
	if decode(t, rec.Body.String())["error"] != "busy" {
		t.Fatalf("got %s", rec.Body.String())
	}
}

func TestWriteUnavailableEscapesTheMessage(t *testing.T) {
	now := time.Now()
	rec := httptest.NewRecorder()
	WriteUnavailable(rec, OpenAI, `<script>"x"</script>`, now.Add(time.Second), now, nil)
	if strings.Contains(rec.Body.String(), "<script>") {
		t.Fatalf("message not escaped: %s", rec.Body.String())
	}
}

func TestResponsesAndOllamaShapes(t *testing.T) {
	if string(Body(Responses, 429, "x")) != string(Body(OpenAI, 429, "x")) {
		t.Fatal("responses body must match the OpenAI body")
	}
	if got := string(Body(Ollama, 502, "boom")); got != `{"error":"boom"}` {
		t.Fatalf("ollama body %s", got)
	}
	if got := string(Frame(Ollama, 502, "boom")); got != "{\"error\":\"boom\"}\n" {
		t.Fatalf("ollama frame %q", got)
	}
	if got := string(Frame(Responses, 504, "t")); !strings.HasPrefix(got, "event: response.failed\ndata: {") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("responses frame %q", got)
	}
	for in, want := range map[string]Format{"claude": Anthropic, "gemini": Gemini, "openai-responses": Responses, "ollama": Ollama, "openai": OpenAI, "": OpenAI} {
		if ForClient(in) != want {
			t.Errorf("ForClient(%q)", in)
		}
	}
}

func TestReadStatus(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{&http.MaxBytesError{Limit: 1}, 413},
		{fmt.Errorf("wrapped: %w", &http.MaxBytesError{Limit: 1}), 413},
		{tests.TimeoutError{}, 408},
		{fmt.Errorf("wrapped: %w", tests.TimeoutError{}), 408},
		{errors.New("reset"), 400},
	}
	for _, c := range cases {
		if got := ReadStatus(c.err); got != c.want {
			t.Errorf("%v: %d, want %d", c.err, got, c.want)
		}
	}
}

func TestWriteUnavailableKeepsTheUpstreamHintsButNotItsRetryAfter(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	extra := http.Header{"Retry-After": {"999"}, "Anthropic-Ratelimit-Requests-Remaining": {"0"}}
	WriteUnavailable(rec, OpenAI, "busy", now.Add(30*time.Second), now, extra)
	if got := rec.Header().Values("Retry-After"); len(got) != 1 || got[0] != "30" {
		t.Errorf("the router's own Retry-After wins and stands alone: %v", got)
	}
	if rec.Header().Get("Anthropic-Ratelimit-Requests-Remaining") != "0" {
		t.Errorf("an upstream rate-limit hint is kept: %v", rec.Header())
	}
	if extra.Get("Retry-After") != "999" {
		t.Error("the caller's headers must not be changed")
	}
}
