package oauth

import (
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/tests"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type handled struct {
	mu    sync.Mutex
	calls []string
}

func (h *handled) record(_ context.Context, _ int, state string, _ Input) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, state)
	return nil
}

func (h *handled) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.calls)
}

func newListeners(t *testing.T) (*Listeners, *handled, *Sessions) {
	t.Helper()
	sessions := NewSessions()
	h := &handled{}
	l := &Listeners{Host: "127.0.0.1", Sessions: sessions, Handle: h.record}
	t.Cleanup(l.Close)
	return l, h, sessions
}

func get(t *testing.T, port int, host, path string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestTheListenerAnswersOnlyAKnownStateOnTheCallbackPathForALoopbackHost(t *testing.T) {
	l, h, sessions := newListeners(t)
	port := freePort(t)
	if !l.Acquire(port, "/auth/callback") {
		t.Fatal("the port was not bound")
	}
	state, _ := sessions.Put(Session{Port: port, Bind: "b"})
	host := "localhost:" + strconv.Itoa(port)

	cases := []struct {
		name, host, path string
		want             int
	}{
		{"unknown state", host, "/auth/callback?code=c&state=nope", 404},
		{"no state", host, "/auth/callback?code=c", 404},
		{"another path", host, "/other?code=c&state=" + state, 404},
		{"the router callback path", host, "/callback?code=c&state=" + state, 404},
		{"a rebinding host", "evil.example:" + strconv.Itoa(port), "/auth/callback?code=c&state=" + state, 404},
	}
	for _, tc := range cases {
		if code, _ := get(t, port, tc.host, tc.path); code != tc.want {
			t.Errorf("%s: %d", tc.name, code)
		}
	}
	if h.count() != 0 {
		t.Fatal("a refused request finished a login")
	}
	if code, body := get(t, port, host, "/auth/callback?code=c&state="+state); code != 200 || body == "" {
		t.Fatalf("the real callback: %d %q", code, body)
	}
	if h.count() != 1 {
		t.Fatalf("%d logins finished", h.count())
	}
}

func TestAStateOfAnotherPortIsRefused(t *testing.T) {
	l, h, sessions := newListeners(t)
	port := freePort(t)
	l.Acquire(port, "/cb")
	other, _ := sessions.Put(Session{Port: port + 1, Bind: "b"})
	if code, _ := get(t, port, "localhost", "/cb?code=c&state="+other); code != 404 || h.count() != 0 {
		t.Fatalf("%d, %d", code, h.count())
	}
}

func TestTwoLoginsShareOneListenerWhichStopsWithTheLast(t *testing.T) {
	l, _, sessions := newListeners(t)
	now := time.Unix(1000, 0)
	l.Now = func() time.Time { return now }
	port := freePort(t)
	if !l.Acquire(port, "/cb") {
		t.Fatal("the listener was not started")
	}
	if !l.Acquire(port, "/cb") {
		t.Fatal("the shared listener was not reused")
	}
	a, _ := sessions.Put(Session{Port: port, Bind: "a"})
	b, _ := sessions.Put(Session{Port: port, Bind: "b"})
	now = now.Add(time.Minute)

	sessions.Begin(a, func(Session) bool { return true })
	sessions.End(a, "c", "")
	l.Release(port)
	if !l.Running(port) {
		t.Fatal("the listener stopped while a login still waited")
	}
	sessions.Begin(b, func(Session) bool { return true })
	sessions.End(b, "c", "")
	l.Release(port)
	if l.Running(port) {
		t.Fatal("the listener outlived its last login")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			ln.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the port was never freed")
}

func TestABusyPortFallsBackToPasting(t *testing.T) {
	l, _, _ := newListeners(t)
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if l.Acquire(busy.Addr().(*net.TCPAddr).Port, "/cb") {
		t.Fatal("a busy port was taken")
	}
}

func TestNothingStartsAfterClose(t *testing.T) {
	l, _, _ := newListeners(t)
	l.Close()
	if l.Acquire(freePort(t), "/cb") {
		t.Fatal("a listener started after Close")
	}
}

func TestAFullCodexLoginOverTheListener(t *testing.T) {
	var seen []string
	srv, _ := tokenServer(t, 200, `{"access_token":"AT","refresh_token":"RT"}`, &seen)
	svc, accounts := newService(t, srv.URL, srv.Client())
	port := freePort(t)
	svc.Registry = fakeRegistryPort(t, srv.URL, port)
	svc.Listeners = &Listeners{Host: "127.0.0.1", Sessions: svc.Sessions, Handle: svc.Listened}
	t.Cleanup(svc.Listeners.Close)

	started, err := svc.Start("codex", "bind", true, 20128)
	if err != nil || started.Paste {
		t.Fatalf("%+v %v", started, err)
	}
	if code, _ := get(t, port, "localhost:"+strconv.Itoa(port), "/auth/callback?code=C&state="+started.State); code != 200 {
		t.Fatalf("callback: %d", code)
	}
	if len(accounts.made) != 1 {
		t.Fatalf("%d accounts", len(accounts.made))
	}
	if st := svc.Sessions.Status(started.State, "bind"); st.State != "done" {
		t.Fatalf("%+v", st)
	}
}

func TestAFailedLoginIsLoggedByItsCodeAndNothingElse(t *testing.T) {
	lg := &tests.Log{}
	sessions := NewSessions()
	l := &Listeners{
		Host: "127.0.0.1", Sessions: sessions, Log: lg,
		Handle: func(context.Context, int, string, Input) error { return &Failure{Code: CodeDenied} },
	}
	t.Cleanup(l.Close)
	port := freePort(t)
	if !l.Acquire(port, "/auth/callback") {
		t.Fatal("the port was not bound")
	}
	state, _ := sessions.Put(Session{Port: port, Bind: "b"})
	if code, _ := get(t, port, "localhost:"+strconv.Itoa(port), "/auth/callback?code=SECRETCODE&state="+state); code != 200 {
		t.Fatalf("status %d", code)
	}
	var seen bool
	for _, line := range lg.Lines() {
		if strings.Contains(line, "SECRETCODE") || strings.Contains(line, state) {
			t.Fatalf("the log line carries login data: %q", line)
		}
		if strings.Contains(line, CodeDenied) {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("a failed login was not logged: %v", lg.Lines())
	}
}
