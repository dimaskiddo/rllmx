package http

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"

	"rllmx/app/llm/stream"
)

// answer serves one response with the given headers and body.
func answer(status int, header map[string]string, body string) nethttp.Handler {
	return nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		if body != "" {
			_, _ = io.WriteString(w, body)
		}
	})
}

// plain is a body of n bytes that does not compress: a client's answer to an
// encoder that compressed nothing must still be larger than the threshold.
func plain(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

func serve(h nethttp.Handler, method, accept string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "/x", strings.NewReader(body))
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// decoded returns the body after undoing the response's encoding.
func decoded(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	switch rec.Header().Get("Content-Encoding") {
	case "", "zstd":
		// A body another layer encoded is passed through as it stands.
		return rec.Body.String()
	case "gzip":
		r, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("the answer is not gzip: %v", err)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("gzip: %v", err)
		}
		return string(out)
	case "br":
		out, err := io.ReadAll(brotli.NewReader(rec.Body))
		if err != nil {
			t.Fatalf("br: %v", err)
		}
		return string(out)
	}
	t.Fatalf("unexpected encoding %q", rec.Header().Get("Content-Encoding"))
	return ""
}

func TestTheCompressionDecision(t *testing.T) {
	big := plain(4096)
	jsonType := map[string]string{"Content-Type": "application/json"}
	cases := []struct {
		name    string
		method  string
		accept  string
		status  int
		header  map[string]string
		body    string
		encoded bool
	}{
		{"json over gzip", "GET", "gzip", 200, jsonType, big, true},
		{"json without an offer stays plain", "GET", "", 200, jsonType, big, false},
		{"identity only", "GET", "identity", 200, jsonType, big, false},
		{"a refused coding", "GET", "gzip;q=0", 200, jsonType, big, false},
		{"a wildcard covers ours", "GET", "*", 200, jsonType, big, true},
		{"a refused wildcard", "GET", "*;q=0", 200, jsonType, big, false},
		{"a lower weight still counts", "GET", "gzip;q=0.1", 200, jsonType, big, true},
		{"a malformed weight does not become one", "GET", "gzip;q=abc", 200, jsonType, big, false},
		{"a negative weight refuses", "GET", "gzip;q=-1", 200, jsonType, big, false},
		{"a weight above one is refused", "GET", "gzip;q=2", 200, jsonType, big, false},
		{"a spaced weight parses", "GET", "gzip ; q=0.5", 200, jsonType, big, true},
		{"HEAD is never compressed", "HEAD", "gzip", 200, jsonType, big, false},
		{"204 has no body", "GET", "gzip", nethttp.StatusNoContent, jsonType, "", false},
		{"304 has no body", "GET", "gzip", nethttp.StatusNotModified, jsonType, "", false},
		{"an existing encoding is left alone", "GET", "gzip", 200, map[string]string{"Content-Type": "application/json", "Content-Encoding": "zstd"}, big, false},
		{"a range is left alone", "GET", "gzip", 200, map[string]string{"Content-Type": "application/json", "Content-Range": "bytes 0-10/20"}, big, false},
		{"no-transform is obeyed", "GET", "gzip", 200, map[string]string{"Content-Type": "application/json", "Cache-Control": "public, no-transform"}, big, false},
		{"a stream is left alone", "GET", "gzip", 200, map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache, no-transform"}, big, false},
		{"ndjson is left alone", "GET", "gzip", 200, map[string]string{"Content-Type": "application/x-ndjson"}, big, false},
		{"audio is left alone", "GET", "gzip", 200, map[string]string{"Content-Type": "audio/mpeg"}, big, false},
		{"an image is left alone", "GET", "gzip", 200, map[string]string{"Content-Type": "image/png"}, big, false},
		{"svg text is compressed", "GET", "gzip", 200, map[string]string{"Content-Type": "image/svg+xml"}, big, true},
		{"a type with a charset", "GET", "gzip", 200, map[string]string{"Content-Type": "Text/HTML; charset=UTF-8"}, big, true},
		{"a +json type", "GET", "gzip", 200, map[string]string{"Content-Type": "application/problem+json"}, big, true},
		{"javascript", "GET", "gzip", 200, map[string]string{"Content-Type": "text/javascript"}, big, true},
		{"no content type at all", "GET", "gzip", 200, nil, big, false},
		{"a short answer is left alone", "GET", "gzip", 200, jsonType, "small", false},
		{"exactly at the threshold", "GET", "gzip", 200, jsonType, plain(compressThreshold), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Compress(true, answer(tc.status, tc.header, tc.body))
			rec := serve(h, tc.method, tc.accept, "")
			// One of ours, or a coding the handler set and the wrapper kept.
			enc := rec.Header().Get("Content-Encoding")
			got := enc == "gzip" || enc == "br"
			if got != tc.encoded {
				t.Fatalf("encoded = %v, want %v (headers %v)", got, tc.encoded, rec.Header())
			}
			if body := decoded(t, rec); body != tc.body {
				t.Fatalf("the body changed: %d bytes, want %d", len(body), len(tc.body))
			}
		})
	}
}

func TestBrotliIsChosenAheadOfGzip(t *testing.T) {
	body := plain(4096)
	cases := []struct{ accept, want string }{
		{"br", "br"},
		{"gzip, br", "br"},
		{"gzip", "gzip"},
		{"br;q=0.5, gzip", "gzip"},
		{"gzip, br;q=0", "gzip"},
		{"*", "br"},
	}
	for _, tc := range cases {
		t.Run(tc.accept, func(t *testing.T) {
			h := Compress(true, answer(200, map[string]string{"Content-Type": "application/json"}, body))
			rec := serve(h, "GET", tc.accept, "")
			if got := rec.Header().Get("Content-Encoding"); got != tc.want {
				t.Fatalf("encoding = %q, want %q", got, tc.want)
			}
			if rec.Header().Get("Content-Length") != "" {
				t.Fatalf("an encoded answer kept its length: %v", rec.Header())
			}
			if !strings.Contains(strings.ToLower(strings.Join(rec.Header().Values("Vary"), ",")), "accept-encoding") {
				t.Fatalf("Vary = %v", rec.Header().Values("Vary"))
			}
			if got := decoded(t, rec); got != body {
				t.Fatalf("the body changed: %d bytes, want %d", len(got), len(body))
			}
		})
	}
}

func TestAnAnswerTheHandlerAlreadyEncodedIsLeftExactlyAsItIs(t *testing.T) {
	// The handler compressed it itself: its encoding, its length and its bytes are
	// all passed through, because encoding it again would be gzip over gzip.
	body := plain(4096)
	h := Compress(true, answer(200, map[string]string{
		"Content-Type": "application/json", "Content-Encoding": "gzip", "Content-Length": strconv.Itoa(len(body)), "Vary": "Accept-Encoding",
	}, body))
	rec := serve(h, "GET", "gzip", "")
	if rec.Header().Get("Content-Encoding") != "gzip" || rec.Body.String() != body {
		t.Fatalf("%v, %d bytes", rec.Header(), rec.Body.Len())
	}
	if n := strings.Count(strings.Join(rec.Header().Values("Vary"), ","), "Accept-Encoding"); n != 1 {
		t.Fatalf("Vary appears %d times", n)
	}
}

func TestAnEncodedAnswerGainsNoVaryItDidNotHave(t *testing.T) {
	// The handler encoded it and said nothing about Vary: this layer must not
	// claim the answer varies when it was never the one choosing the form.
	h := Compress(true, answer(200, map[string]string{"Content-Type": "application/json", "Content-Encoding": "zstd"}, plain(4096)))
	rec := serve(h, "GET", "gzip", "")
	if got := rec.Header().Values("Vary"); len(got) != 0 {
		t.Fatalf("Vary = %q on an answer another layer encoded", got)
	}
}

func TestACompressedAnswerDropsItsLengthAndKeepsItsBytes(t *testing.T) {
	body := plain(8192)
	h := Compress(true, answer(200, map[string]string{
		"Content-Type": "application/json", "Content-Length": strconv.Itoa(len(body)), "ETag": `"abc"`,
	}, body))
	rec := serve(h, "GET", "gzip", "")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("%v", rec.Header())
	}
	if got := rec.Header().Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length survived: %q", got)
	}
	if got := rec.Header().Get("ETag"); got != `W/"abc"` {
		t.Fatalf("ETag = %q, want it weakened", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q", got)
	}
	if decoded(t, rec) != body {
		t.Fatal("the bytes changed")
	}
}

func TestAPlainAnswerKeepsItsLengthAndStillVaries(t *testing.T) {
	body := "short"
	h := Compress(true, answer(200, map[string]string{"Content-Type": "application/json"}, body))
	rec := serve(h, "GET", "gzip", "")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("a short answer was compressed")
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
		t.Fatalf("Content-Length = %q", got)
	}
	if got := rec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("Vary = %q on a compressible type", got)
	}
}

func TestVaryKeepsWhatAnotherLayerSet(t *testing.T) {
	h := Compress(true, answer(200, map[string]string{
		"Content-Type": "application/json", "Vary": "Access-Control-Request-Headers",
	}, plain(4096)))
	rec := serve(h, "GET", "gzip", "")
	all := strings.Join(rec.Header().Values("Vary"), ",")
	if !strings.Contains(all, "Access-Control-Request-Headers") || !strings.Contains(all, "Accept-Encoding") {
		t.Fatalf("Vary = %q", all)
	}
	if n := strings.Count(strings.ToLower(all), "accept-encoding"); n != 1 {
		t.Fatalf("Accept-Encoding appears %d times in %q", n, all)
	}
}

func TestDisablingCompressionSendsNothingEncoded(t *testing.T) {
	h := Compress(false, answer(200, map[string]string{"Content-Type": "application/json"}, plain(8192)))
	rec := serve(h, "GET", "gzip", "")
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.Len() < compressThreshold {
		t.Fatalf("%v, %d bytes", rec.Header(), rec.Body.Len())
	}
}

func TestAStreamIsFlushedFrameByFrameAndNeverEncoded(t *testing.T) {
	// A live server, because a recorder has no connection to flush to.
	release := make(chan struct{})
	h := Compress(true, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.WriteHeader(200)
		w.(nethttp.Flusher).Flush()
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(nethttp.Flusher).Flush()
		<-release
		_, _ = io.WriteString(w, "data: two\n\n")
		w.(nethttp.Flusher).Flush()
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	defer close(release)

	req, _ := nethttp.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("a stream was encoded: %q", got)
	}
	buf := make([]byte, 64)
	read := func() string {
		n, err := res.Body.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(buf[:n])
	}
	got := read()
	if !strings.Contains(got, "data: one") {
		// net/http may hand back the first frame together; read once more.
		got += read()
	}
	if !strings.Contains(got, "data: one") {
		t.Fatalf("the first frame did not arrive before the handler finished: %q", got)
	}
}

// TestAStreamThroughTheSinkSendsEachFrameAndOneStatus drives a real stream, so
// the flush the sink makes through the connection below this writer meets a
// status line that has already gone out. A second one is net/http's
// "superfluous response.WriteHeader", and a frame held in the buffer until the
// threshold would miss the flush that was meant to carry it.
func TestAStreamThroughTheSinkSendsEachFrameAndOneStatus(t *testing.T) {
	release := make(chan struct{})
	h := Compress(true, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		s, err := stream.NewSSE(w, stream.Options{})
		if err != nil {
			t.Errorf("stream: %v", err)
			return
		}
		if err := s.Event("tick", "one"); err != nil {
			t.Errorf("event: %v", err)
			return
		}
		<-release
	}))
	var logged bytes.Buffer
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(&logged, "", 0)
	srv.Start()
	defer srv.Close()
	// The handler must be released on every path, including a Fatal here.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	req, _ := nethttp.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := nethttp.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if got := res.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("a stream was encoded: %q", got)
	}
	line := make([]byte, 64)
	frames := make(chan string, 1)
	go func() {
		n, _ := res.Body.Read(line)
		frames <- string(line[:n])
	}()
	select {
	case frame := <-frames:
		if !strings.Contains(frame, "data: one") {
			t.Fatalf("the first frame: %q", frame)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the first frame was held past the flush the sink made")
	}
	close(release)
	_, _ = io.Copy(io.Discard, res.Body)
	if strings.Contains(logged.String(), "superfluous") {
		t.Fatalf("the status line went out twice: %s", logged.String())
	}
}

func TestAWriteThatNeverReachesTheThresholdIsSentPlain(t *testing.T) {
	h := Compress(true, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	rec := serve(h, "GET", "gzip", "")
	if rec.Header().Get("Content-Encoding") != "" || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("%v %q", rec.Header(), rec.Body.String())
	}
}

func TestAnAnswerWithNoBodySendsItsStatus(t *testing.T) {
	for _, status := range []int{nethttp.StatusNoContent, nethttp.StatusOK} {
		h := Compress(true, answer(status, map[string]string{"Content-Type": "application/json"}, ""))
		rec := serve(h, "GET", "gzip", "")
		if rec.Code != status || rec.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%d: %d %v", status, rec.Code, rec.Header())
		}
		if status == nethttp.StatusNoContent && rec.Header().Get("Content-Length") != "" {
			t.Fatal("a 204 carried a Content-Length")
		}
	}
}

func TestAPanicBeforeTheThresholdStillReachesTheClient(t *testing.T) {
	h := Compress(true, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"partial":`)
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	func() {
		defer func() { _ = recover() }()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	}()
	if got := rec.Body.String(); got != `{"partial":` {
		t.Fatalf("a panicked answer lost its bytes: %q", got)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Fatal("a panicked answer was encoded")
	}
}

// deadlineRecorder notes whether SetWriteDeadline reached it.
type deadlineRecorder struct {
	nethttp.ResponseWriter
	set bool
}

func (d *deadlineRecorder) SetWriteDeadline(time.Time) error { d.set = true; return nil }

func TestTheWriteDeadlineReachesTheConnectionThroughTheWrapper(t *testing.T) {
	inner := &deadlineRecorder{ResponseWriter: httptest.NewRecorder()}
	h := Compress(true, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, plain(4096))
	}))
	h.ServeHTTP(inner, httptest.NewRequest("GET", "/x", nil))
	w := &compressWriter{ResponseWriter: inner, enc: "gzip"}
	if err := nethttp.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second)); err != nil || !inner.set {
		t.Fatalf("the deadline did not reach the connection: %v, set=%v", err, inner.set)
	}
}

func TestTheUnwrapChainReachesTheRealWriter(t *testing.T) {
	var real nethttp.ResponseWriter = httptest.NewRecorder()
	w := &compressWriter{ResponseWriter: real}
	if w.Unwrap() != real {
		t.Fatal("Unwrap does not return the writer below")
	}
}

// FuzzAcceptEncoding feeds the parser whatever a client sends. It must never
// panic, and an encoding it chooses is one it actually offers.
func FuzzAcceptEncoding(f *testing.F) {
	for _, s := range []string{"gzip", "br, gzip", "*", "gzip;q=0", " identity , *;q=0.5 ", "", ",,,", "gzip;q=abc", "gzip;q=2", "br;q=0.5, gzip;q=0.9"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		enc := acceptedEncoder(raw)
		if enc == "" {
			return
		}
		for _, e := range encoders {
			if e.name == enc {
				return
			}
		}
		t.Fatalf("%q chose %q, which nothing offers", raw, enc)
	})
}

// FuzzCompressibleContentType reads any Content-Type. A type it accepts is a
// text type, JSON, XML or an SVG, whatever the case or parameters; audio, video,
// a raster image and anything untyped are refused.
func FuzzCompressibleContentType(f *testing.F) {
	for _, s := range []string{"application/json", "text/event-stream", "application/x-ndjson", "audio/mpeg", "image/png", "TEXT/HTML; charset=utf-8", "", "application/problem+json"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, ct string) {
		if !compressible(ct) {
			return
		}
		base, _, _ := strings.Cut(strings.ToLower(ct), ";")
		base = strings.TrimSpace(base)
		textual := strings.HasPrefix(base, "text/") || strings.HasPrefix(base, "application/json") ||
			strings.HasPrefix(base, "application/xml") || strings.HasPrefix(base, "application/javascript") ||
			strings.HasPrefix(base, "image/svg+xml") || strings.HasSuffix(base, "+json") || strings.HasSuffix(base, "+xml")
		if !textual {
			t.Fatalf("%q was accepted and is not a text type", ct)
		}
	})
}
