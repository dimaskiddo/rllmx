package commands

import (
	"context"
	"io"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goravel/framework/contracts/console"

	"rllmx/app/settings"
	"rllmx/tests"
)

// fakeConsole records what a command prints. Prompts and every other method
// panic, which is how a test notices a command that asks for input.
type fakeConsole struct {
	console.Context
	lines []string
	errs  []string
}

func (*fakeConsole) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*fakeConsole) Done() <-chan struct{}       { return nil }
func (*fakeConsole) Err() error                  { return nil }
func (*fakeConsole) Value(any) any               { return nil }
func (f *fakeConsole) Line(m string)             { f.lines = append(f.lines, m) }
func (f *fakeConsole) Info(m string)             { f.lines = append(f.lines, m) }
func (f *fakeConsole) Error(m string)            { f.errs = append(f.errs, m) }
func (f *fakeConsole) output() string            { return strings.Join(f.lines, "\n") }

var _ context.Context = (*fakeConsole)(nil)

func settingsFor(t *testing.T, srv *httptest.Server) func() (*settings.Server, error) {
	t.Helper()
	host, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	return func() (*settings.Server, error) {
		m := tests.ServerValues()
		m["http.host"], m["http.port"] = host, port
		return settings.LoadServer(tests.Config{Values: m})
	}
}

func TestVersionPrintsTheVersionAndTheCommit(t *testing.T) {
	c := &fakeConsole{}
	if err := (&Version{Version: "1.2.3", Commit: "abc1234"}).Handle(c); err != nil {
		t.Fatal(err)
	}
	if c.output() != "rllmx 1.2.3 (abc1234)" {
		t.Fatalf("output %q", c.output())
	}
}

func TestVersionOfADevelopmentBuild(t *testing.T) {
	c := &fakeConsole{}
	_ = (&Version{Version: "dev", Commit: "none"}).Handle(c)
	if c.output() != "rllmx dev (none)" {
		t.Fatalf("output %q", c.output())
	}
}

func TestHealthSucceedsOnAHealthyServer(t *testing.T) {
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path != "/api/health" {
			w.WriteHeader(404)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	c := &fakeConsole{}
	if err := (&Health{Settings: settingsFor(t, srv)}).Handle(c); err != nil {
		t.Fatalf("a healthy server: %v", err)
	}
	if c.output() == "" {
		t.Fatal("nothing was printed")
	}
}

func TestHealthFailsOnAnUnhealthyServer(t *testing.T) {
	for _, body := range []string{`{"ok":false}`, `not json`, ``} {
		srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { _, _ = io.WriteString(w, body) }))
		err := (&Health{Settings: settingsFor(t, srv)}).Handle(&fakeConsole{})
		srv.Close()
		if err == nil {
			t.Errorf("body %q counted as healthy", body)
		}
	}
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) { w.WriteHeader(503) }))
	defer srv.Close()
	if err := (&Health{Settings: settingsFor(t, srv)}).Handle(&fakeConsole{}); err == nil {
		t.Fatal("a 503 counted as healthy")
	}
}

func TestHealthFailsWhenNothingListens(t *testing.T) {
	srv := httptest.NewServer(nethttp.NotFoundHandler())
	load := settingsFor(t, srv)
	srv.Close()
	if err := (&Health{Settings: load}).Handle(&fakeConsole{}); err == nil {
		t.Fatal("a closed port counted as healthy")
	}
}

func TestHealthFailsOnInvalidSettings(t *testing.T) {
	bad := func() (*settings.Server, error) {
		m := tests.ServerValues()
		m["http.port"] = "0"
		return settings.LoadServer(tests.Config{Values: m})
	}
	err := (&Health{Settings: bad}).Handle(&fakeConsole{})
	if err == nil || !strings.Contains(err.Error(), "APP_PORT") {
		t.Fatalf("error = %v", err)
	}
}

func TestCommandsHaveTheirOwnSignaturesAndNoPrompts(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range []console.Command{&Health{}, &Version{}} {
		if c.Signature() == "" || seen[c.Signature()] || strings.Contains(c.Signature(), "app:") {
			t.Errorf("signature %q", c.Signature())
		}
		seen[c.Signature()] = true
		if c.Description() == "" || c.Description() == "Command description" {
			t.Errorf("%s has no description", c.Signature())
		}
	}
	if !seen["health"] || !seen["version"] {
		t.Fatalf("signatures %v", seen)
	}
}
