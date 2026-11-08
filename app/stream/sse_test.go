package stream

import (
	"bufio"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/tests"
)

func TestHeadersAndFirstFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	s, err := NewSSE(rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Data("x"); err != nil {
		t.Fatal(err)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "text/event-stream" || h.Get("Cache-Control") != "no-cache, no-transform" || h.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers: %v", h)
	}
	if rec.Code != 200 || !rec.Flushed {
		t.Fatalf("code %d flushed %v", rec.Code, rec.Flushed)
	}
	if h.Get("Content-Length") != "" {
		t.Fatal("a stream must not declare a length")
	}
}

func TestFramingOfEventsAndData(t *testing.T) {
	rec := httptest.NewRecorder()
	s, _ := NewSSE(rec, Options{})
	steps := []func() error{
		func() error { return s.Data(`{"a":1}`) },
		func() error { return s.Event("message_start", `{"b":2}`) },
		func() error { return s.Data("line1\nline2\r\nline3") },
		func() error { return s.Data("") },
		func() error { return s.Event("ping", "") },
	}
	for i, f := range steps {
		if err := f(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	want := "data: {\"a\":1}\n\n" +
		"event: message_start\ndata: {\"b\":2}\n\n" +
		"data: line1\ndata: line2\ndata: line3\n\n" +
		"data: \n\n" +
		"event: ping\ndata: \n\n"
	if rec.Body.String() != want {
		t.Fatalf("got  %q\nwant %q", rec.Body.String(), want)
	}
}

func TestRawWritesAPrebuiltFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	s, _ := NewSSE(rec, Options{})
	if err := s.Raw([]byte("event: error\ndata: {}\n\n")); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "event: error\ndata: {}\n\n" {
		t.Fatalf("got %q", rec.Body.String())
	}
}

func TestEventNamesCannotInjectFrames(t *testing.T) {
	for _, bad := range []string{"a\nb", "a\rb", "a\r\nb", "x\ndata: forged", "\n"} {
		rec := httptest.NewRecorder()
		s, _ := NewSSE(rec, Options{})
		if err := s.Event(bad, "d"); !errors.Is(err, ErrInvalidEventName) {
			t.Errorf("%q: err = %v", bad, err)
		}
		if strings.Contains(rec.Body.String(), "forged") || strings.Contains(rec.Body.String(), "event:") {
			t.Errorf("%q wrote %q", bad, rec.Body.String())
		}
	}
}

func TestDataWithSSEFieldNamesInsideIsPlainText(t *testing.T) {
	rec := httptest.NewRecorder()
	s, _ := NewSSE(rec, Options{})
	_ = s.Data("event: forged\nid: 1\nretry: 0")
	for line := range strings.SplitSeq(strings.TrimSuffix(rec.Body.String(), "\n\n"), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			t.Errorf("a line escaped the data field: %q", line)
		}
	}
}

func TestBareCRInDataSplitsIntoDataLines(t *testing.T) {
	rec := httptest.NewRecorder()
	s, _ := NewSSE(rec, Options{})
	_ = s.Data("a\rb")
	if rec.Body.String() != "data: a\ndata: b\n\n" {
		t.Fatalf("got %q: a bare CR is a line end for SSE parsers and must not stay inside a data field", rec.Body.String())
	}
}

type noFlush struct{ http.ResponseWriter }

func (noFlush) Unwrap() http.ResponseWriter { return nil }

func TestAWriterThatCannotFlushIsRefused(t *testing.T) {
	if _, err := NewSSE(noFlush{httptest.NewRecorder()}, Options{}); !errors.Is(err, ErrNoFlush) {
		t.Fatalf("err = %v", err)
	}
}

// failingWriter reports failures the way net/http's real writer does.
type failingWriter struct {
	*httptest.ResponseRecorder
	mu       sync.Mutex
	flushErr error
	writeErr error
	flushes  int
}

func (f *failingWriter) FlushError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushes++
	return f.flushErr
}

func (f *failingWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.ResponseRecorder.Write(p)
}

func (f *failingWriter) set(flushErr, writeErr error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flushErr, f.writeErr = flushErr, writeErr
}

func TestFlushErrorsAreReturned(t *testing.T) {
	fw := &failingWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: io.ErrClosedPipe}
	s, err := NewSSE(fw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Data("x"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("a failed flush after a write must surface: %v", err)
	}
}

func TestWriteErrorsAreReturned(t *testing.T) {
	fw := &failingWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: io.ErrClosedPipe}
	s, _ := NewSSE(fw, Options{})
	for name, f := range map[string]func() error{
		"data":  func() error { return s.Data("x") },
		"event": func() error { return s.Event("e", "x") },
		"raw":   func() error { return s.Raw([]byte("x")) },
	} {
		if err := f(); !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestOnceFailedEveryLaterCallFails(t *testing.T) {
	fw := &failingWriter{ResponseRecorder: httptest.NewRecorder(), writeErr: io.ErrClosedPipe}
	s, _ := NewSSE(fw, Options{})
	_ = s.Data("x")
	fw.set(nil, nil)
	if err := s.Data("y"); err == nil {
		t.Fatal("the stream recovered after the client went away: callers would keep burning upstream tokens")
	}
}

func TestAWriteAfterAFailedFlushStaysFailed(t *testing.T) {
	fw := &failingWriter{ResponseRecorder: httptest.NewRecorder()}
	s, _ := NewSSE(fw, Options{})
	fw.set(io.ErrClosedPipe, nil)
	if err := s.Data("x"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush: %v", err)
	}
	fw.set(nil, nil)
	before := fw.flushes
	if err := s.Data("y"); err == nil {
		t.Fatal("the stream recovered after a failure")
	}
	if fw.flushes != before {
		t.Fatal("the stream kept talking to a connection that already failed")
	}
}

func TestClientDisconnectIsDetectedOverARealConnection(t *testing.T) {
	started := make(chan struct{})
	result := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, err := NewSSE(w, Options{})
		if err != nil {
			result <- err
			return
		}
		_ = s.Data("")
		close(started)
		payload := strings.Repeat("x", 64<<10)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if err := s.Data(payload); err != nil {
				result <- err
				return
			}
		}
		result <- errors.New("writes kept succeeding after the client closed")
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	<-started
	_ = conn.Close()
	select {
	case err := <-result:
		if err == nil || strings.Contains(err.Error(), "kept succeeding") {
			t.Fatalf("%v", err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("the handler never noticed the client was gone")
	}
}

func TestSmallEventsReachTheClientImmediately(t *testing.T) {
	got := make(chan string, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, _ := NewSSE(w, Options{})
		_ = s.Event("hello", "world")
		<-release
	}))
	defer srv.Close()
	defer close(release)
	go func() {
		resp, err := http.Get(srv.URL)
		if err != nil {
			got <- err.Error()
			return
		}
		defer resp.Body.Close()
		line, _ := bufio.NewReader(resp.Body).ReadString('\n')
		got <- line
	}()
	select {
	case line := <-got:
		if line != "event: hello\n" {
			t.Fatalf("first line %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a small event sat in a buffer instead of reaching the client")
	}
}

type deadlineWriter struct {
	*httptest.ResponseRecorder
	mu  sync.Mutex
	set []time.Time
	log *[]string
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.set = append(d.set, t)
	if d.log != nil {
		*d.log = append(*d.log, "deadline")
	}
	return nil
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	if d.log != nil {
		d.mu.Lock()
		*d.log = append(*d.log, "write")
		d.mu.Unlock()
	}
	return d.ResponseRecorder.Write(p)
}

func (d *deadlineWriter) FlushError() error { return nil }

func TestEachWriteGetsItsOwnDeadline(t *testing.T) {
	rw := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	s, _ := NewSSE(rw, Options{WriteTimeout: 50 * time.Millisecond})
	_ = s.Data("a")
	time.Sleep(120 * time.Millisecond)
	_ = s.Data("b")
	rw.mu.Lock()
	defer rw.mu.Unlock()
	if len(rw.set) < 2 {
		t.Fatalf("%d deadlines set for 2 writes", len(rw.set))
	}
	last := rw.set[len(rw.set)-1]
	if !last.After(time.Now()) || time.Until(last) > 60*time.Millisecond {
		t.Fatalf("the second write got a stale or distant deadline: %v from now", time.Until(last))
	}
	if gap := last.Sub(rw.set[0]); gap < 100*time.Millisecond {
		t.Fatalf("deadlines %v apart: the deadline was set once, not before each write", gap)
	}
}

func TestEachWriteSetsTheDeadlineBeforeWriting(t *testing.T) {
	var order []string
	rw := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), log: &order}
	s, _ := NewSSE(rw, Options{WriteTimeout: time.Second})
	order = order[:0]
	_ = s.Data("a")
	if len(order) < 2 || order[0] != "deadline" || order[1] != "write" {
		t.Fatalf("order %v: the deadline must be set immediately before each write", order)
	}
}

func TestZeroWriteTimeoutMeansNoDeadline(t *testing.T) {
	rw := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	s, _ := NewSSE(rw, Options{})
	_ = s.Data("a")
	if len(rw.set) != 0 {
		t.Fatal("a deadline was set although none was asked for")
	}
}

func TestDefaultWriteTimeoutIsSixtySeconds(t *testing.T) {
	rw := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	s, _ := NewSSE(rw, Options{WriteTimeout: DefaultWriteTimeout})
	_ = s.Data("a")
	if d := time.Until(rw.set[0]); d < 55*time.Second || d > 61*time.Second {
		t.Fatalf("deadline in %v", d)
	}
}

func TestARejectedWriteDeadlineIsLoggedOnce(t *testing.T) {
	l := &tests.Log{}
	rec := httptest.NewRecorder()
	s, _ := NewSSE(rec, Options{WriteTimeout: time.Second, Log: l})
	for range 3 {
		if err := s.Data("x"); err != nil {
			t.Fatal(err)
		}
	}
	if lines := l.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "write deadline") {
		t.Fatalf("log = %q", lines)
	}
	if _, err := NewSSE(httptest.NewRecorder(), Options{WriteTimeout: time.Second}); err != nil {
		t.Fatalf("a nil logger must be silent: %v", err)
	}
}

func TestConcurrentWritersDoNotInterleaveFrames(t *testing.T) {
	rec := httptest.NewRecorder()
	s, _ := NewSSE(rec, Options{})
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 50 {
				_ = s.Event("e", strings.Repeat("z", 100))
			}
		})
	}
	wg.Wait()
	frames := strings.Split(strings.TrimSuffix(rec.Body.String(), "\n\n"), "\n\n")
	if len(frames) != 1000 {
		t.Fatalf("%d frames", len(frames))
	}
	for _, f := range frames {
		if f != "event: e\ndata: "+strings.Repeat("z", 100) {
			t.Fatalf("corrupt frame %q", f)
		}
	}
}

// layer can flush but cannot report a flush error, like Gin's writer: it hides
// failures from a controller built on it.
type layer struct {
	http.ResponseWriter
	headerNowCalled *bool
}

func (l layer) Unwrap() http.ResponseWriter { return l.ResponseWriter }

func (l layer) Flush() {
	if f, ok := l.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (l layer) WriteHeaderNow() {
	if l.headerNowCalled != nil {
		*l.headerNowCalled = true
	}
}

func TestFlushErrorSurfacesThroughSeveralWrapperLayers(t *testing.T) {
	var w http.ResponseWriter = &failingWriter{ResponseRecorder: httptest.NewRecorder(), flushErr: io.ErrClosedPipe}
	for range 4 {
		w = layer{ResponseWriter: w}
	}
	s, err := NewSSE(w, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Data("x"); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("a failure behind 4 wrappers must still surface: %v", err)
	}
}

type nilUnwrap struct{ http.ResponseWriter }

func (nilUnwrap) Unwrap() http.ResponseWriter { return nil }
func (n nilUnwrap) Flush()                    { n.ResponseWriter.(http.Flusher).Flush() }

func TestAWrapperThatUnwrapsToNilStopsTheWalk(t *testing.T) {
	rec := httptest.NewRecorder()
	s, err := NewSSE(nilUnwrap{rec}, Options{})
	if err != nil {
		t.Fatalf("a wrapper returning nil from Unwrap must be used as it is: %v", err)
	}
	_ = s.Data("x")
	if rec.Body.String() != "data: x\n\n" {
		t.Fatalf("body %q", rec.Body.String())
	}
}

func TestStatusLineIsSentBeforeTheFirstEvent(t *testing.T) {
	called := false
	rec := httptest.NewRecorder()
	if _, err := NewSSE(layer{ResponseWriter: rec, headerNowCalled: &called}, Options{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("WriteHeaderNow was not called: a wrapper that defers the status line would send nothing until the first event")
	}
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestAStaleContentLengthIsRemoved(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Length", "12")
	if _, err := NewSSE(rec, Options{}); err != nil {
		t.Fatal(err)
	}
	if rec.Header().Get("Content-Length") != "" {
		t.Fatal("a Content-Length set earlier would cut the stream short")
	}
}

type recordingWriter struct {
	*httptest.ResponseRecorder
	headerAtStatus   http.Header
	statusWrites     int
	flushBeforeState bool
}

func (r *recordingWriter) WriteHeader(code int) {
	r.statusWrites++
	if r.headerAtStatus == nil {
		r.headerAtStatus = r.Header().Clone()
	}
	r.ResponseRecorder.WriteHeader(code)
}

func (r *recordingWriter) FlushError() error {
	if r.statusWrites == 0 {
		r.flushBeforeState = true
	}
	return nil
}

func TestNothingIsFlushedBeforeTheStreamHeadersAreSet(t *testing.T) {
	rw := &recordingWriter{ResponseRecorder: httptest.NewRecorder()}
	if _, err := NewSSE(rw, Options{}); err != nil {
		t.Fatal(err)
	}
	if rw.flushBeforeState {
		t.Fatal("a flush went out before the status line: the response started without its stream headers")
	}
	if rw.headerAtStatus.Get("Content-Type") != "text/event-stream" || rw.headerAtStatus.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("the status line was sent with headers %v", rw.headerAtStatus)
	}
}

func TestTheStatusLineIsWrittenOnce(t *testing.T) {
	rw := &recordingWriter{ResponseRecorder: httptest.NewRecorder()}
	s, _ := NewSSE(rw, Options{})
	_ = s.Data("x")
	if rw.statusWrites != 1 {
		t.Fatalf("WriteHeader was called %d times", rw.statusWrites)
	}
}
