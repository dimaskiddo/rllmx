package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"rllmx/app/llm/upstream"
)

const testCatalog = `{"anthropic":{"models":{"claude-opus-5":{"modalities":{"input":["image"]},"limit":{"context":300000,"output":32000}}}}}`

func testCapabilities(t *testing.T) *upstream.Capabilities {
	t.Helper()
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	c, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// catalogStub answers the catalog call.
type catalogStub struct {
	status  int
	body    string
	headers []http.Header
	// entered, when set, is closed on the first call and the call waits for
	// release, so a second sync can race the first.
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *catalogStub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.headers = append(s.headers, r.Header.Clone())
	if s.entered != nil {
		s.once.Do(func() { close(s.entered) })
		<-s.release
	}
	status := s.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{}, Body: io.NopCloser(strings.NewReader(s.body))}, nil
}

func newTestSync(t *testing.T, c *upstream.Capabilities, body string) (*CatalogSync, string) {
	t.Helper()
	dir := t.TempDir()
	stub := &catalogStub{body: body}
	return newCatalogSync(c, dir, func() *http.Client { return &http.Client{Transport: stub} }, nil), dir
}

func TestASyncWritesTheFileAndInstallsIt(t *testing.T) {
	c := testCapabilities(t)
	s, dir := newTestSync(t, c, testCatalog)
	res, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "updated" || res.Models == 0 || res.Bytes == 0 {
		t.Fatalf("result: %+v", res)
	}
	path := filepath.Join(dir, upstream.OverlayFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode: %v", info.Mode().Perm())
	}
	o := c.Overlay()
	if o == nil || o.Version != upstream.OverlayVersion {
		t.Fatalf("the overlay was not installed: %+v", o)
	}
	if got := c.For("claude", "claude-opus-5"); got.ContextWindow != 300000 {
		t.Errorf("the catalog did not refine the answer: %+v", got)
	}
	st := s.State()
	if st.LastError != "" || st.Catalog == nil || st.Catalog.Models == 0 || st.FileVersion != upstream.OverlayVersion {
		t.Fatalf("state: %+v", st)
	}
	if st.Running {
		t.Error("a finished sync is still marked running")
	}
}

func TestUnchangedLeavesTheFileAndTheCatalogAlone(t *testing.T) {
	c := testCapabilities(t)
	dir := t.TempDir()
	stub := &catalogStub{status: http.StatusNotModified}
	s := newCatalogSync(c, dir, func() *http.Client { return &http.Client{Transport: stub} }, nil)
	// A stored version and ETag make the next call conditional.
	if err := os.WriteFile(filepath.Join(dir, upstream.OverlayFile),
		[]byte(`{"v":3,"etag":"\"v1\"","syncedAt":1,"models":{},"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Load()
	before, err := os.ReadFile(filepath.Join(dir, upstream.OverlayFile))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "unchanged" {
		t.Fatalf("result: %+v", res)
	}
	if len(stub.headers) != 1 || stub.headers[0].Get("If-None-Match") != `"v1"` {
		t.Fatalf("the stored ETag was not sent: %v", stub.headers)
	}
	after, _ := os.ReadFile(filepath.Join(dir, upstream.OverlayFile))
	if string(before) != string(after) {
		t.Error("an unchanged sync rewrote the file")
	}
}

func TestABadCatalogIsAFailureWithNothingWritten(t *testing.T) {
	c := testCapabilities(t)
	s, dir := newTestSync(t, c, "{not json")
	if _, err := s.Run(context.Background()); err == nil {
		t.Fatal("a bad catalog was accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, upstream.OverlayFile)); !errors.Is(err, os.ErrNotExist) {
		t.Error("a failed sync wrote a file")
	}
	if c.Overlay() != nil {
		t.Error("a failed sync installed a catalog")
	}
	if st := s.State(); st.LastError == "" {
		t.Error("a failed sync recorded no reason")
	}
}

func TestASecondSyncIsRefusedWhileOneRuns(t *testing.T) {
	c := testCapabilities(t)
	dir := t.TempDir()
	stub := &catalogStub{body: testCatalog, entered: make(chan struct{}), release: make(chan struct{})}
	s := newCatalogSync(c, dir, func() *http.Client { return &http.Client{Transport: stub} }, nil)
	done := make(chan error, 1)
	go func() {
		_, err := s.Run(context.Background())
		done <- err
	}()
	<-stub.entered
	if _, err := s.Run(context.Background()); !errors.Is(err, upstream.ErrSyncRunning) {
		t.Fatalf("second sync: %v", err)
	}
	if !s.State().Running {
		t.Error("the running state was not reported")
	}
	close(stub.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLoadRefusesAFileThisBuildDidNotWrite(t *testing.T) {
	c := testCapabilities(t)
	dir := t.TempDir()
	s := newCatalogSync(c, dir, nil, nil)
	for name, body := range map[string]string{
		"reference version": `{"v":2,"models":{},"providers":{}}`,
		"garbage":           "not json",
	} {
		if err := os.WriteFile(filepath.Join(dir, upstream.OverlayFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		s.Load()
		if c.Overlay() != nil {
			t.Errorf("%s: a file was installed", name)
		}
	}
	// A missing file is not an error either.
	if err := os.Remove(filepath.Join(dir, upstream.OverlayFile)); err != nil {
		t.Fatal(err)
	}
	s.Load()
	if c.Overlay() != nil {
		t.Error("a missing file installed something")
	}
}

func TestTheRunnerLoadsTheFileWithTheTimerOff(t *testing.T) {
	c := testCapabilities(t)
	dir := t.TempDir()
	s := newCatalogSync(c, dir, nil, nil)
	o := &upstream.Overlay{Version: upstream.OverlayVersion, Models: map[string]upstream.Modalities{}, Providers: map[string]map[string]upstream.Limits{}}
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, upstream.OverlayFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r := newCatalogRunner(&fakeApp{}, s, false)
	if err := r.Run(); err != nil {
		t.Fatal(err)
	}
	if c.Overlay() == nil {
		t.Fatal("the file was not loaded")
	}
	if err := r.Shutdown(); err != nil {
		t.Fatal(err)
	}
}

func TestAForeignFileEarnsAFullFetch(t *testing.T) {
	c := testCapabilities(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, upstream.OverlayFile),
		[]byte(`{"v":2,"etag":"\"theirs\"","models":{},"providers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := &catalogStub{body: testCatalog, status: http.StatusNotModified}
	s := newCatalogSync(c, dir, func() *http.Client { return &http.Client{Transport: stub} }, nil)
	s.Load()
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stub.headers) != 1 || stub.headers[0].Get("If-None-Match") != "" {
		t.Fatalf("another build's ETag was reused: %v", stub.headers)
	}
}
