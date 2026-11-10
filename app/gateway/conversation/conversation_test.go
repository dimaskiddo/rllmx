package conversation

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore() (*Store, *clock) {
	c := &clock{t: time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)}
	return NewStore(c.now), c
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestResolveOrderOfSources(t *testing.T) {
	s, _ := newStore()
	cases := []struct {
		name string
		in   Input
		want string
	}{
		{"claude suffix", Input{Body: []byte(`{"metadata":{"user_id":"user_abc_account__session_0a1b-2c"}}`)}, "claude:0a1b-2c"},
		{"claude json", Input{Body: []byte(`{"metadata":{"user_id":"{\"session_id\":\"sess-9\"}"}}`)}, "claude:sess-9"},
		{"claude header", Input{Header: hdr("X-Claude-Code-Session-Id", "hdr-1")}, "claude:hdr-1"},
		{"claude body beats claude header", Input{Header: hdr("X-Claude-Code-Session-Id", "hdr-1"), Body: []byte(`{"metadata":{"user_id":"u_session_aa"}}`)}, "claude:aa"},
		{"claude beats a plain header", Input{Header: hdr("X-Session-Id", "plain", "X-Claude-Code-Session-Id", "cc")}, "claude:cc"},
		{"antigravity", Input{Body: []byte(`{"request":{"sessionId":"-123"}}`)}, "antigravity:-123"},
		{"antigravity request id", Input{Body: []byte(`{"requestId":"agent/0a1b2c3d-0a1b-0a1b-0a1b-0a1b2c3d4e5f/9"}`)}, "antigravity:0a1b2c3d-0a1b-0a1b-0a1b-0a1b2c3d4e5f"},
		{"x-session-id", Input{Header: hdr("X-Session-Id", "s1", "Session-Id", "s2")}, "s1"},
		{"session-id", Input{Header: hdr("Session-Id", "s2", "X-Amp-Thread-Id", "t")}, "s2"},
		{"amp thread", Input{Header: hdr("X-Amp-Thread-Id", "T-1", "X-Client-Request-Id", "r")}, "T-1"},
		{"request id header", Input{Header: hdr("X-Client-Request-Id", "req-7")}, "req-7"},
		{"header beats body", Input{Header: hdr("X-Client-Request-Id", "req-7"), Body: []byte(`{"prompt_cache_key":"k"}`)}, "req-7"},
		{"prompt cache key", Input{Body: []byte(`{"prompt_cache_key":"pck","session_id":"x"}`)}, "pck"},
		{"body session id", Input{Body: []byte(`{"session_id":"bs","conversation_id":"c"}`)}, "bs"},
		{"conversation id", Input{Body: []byte(`{"conversation_id":"conv-1"}`)}, "conv-1"},
		{"plain user id", Input{Body: []byte(`{"metadata":{"user_id":"alice"}}`)}, "alice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := s.Resolve(c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestACandidateThatIsNotAPlainTokenIsLeftOut(t *testing.T) {
	s, _ := newStore()
	bad := []string{"has space", "new\nline", "semi;colon", strings.Repeat("a", 257), "", "   ", "a/b", "héllo"}
	for _, v := range bad {
		got := s.Resolve(Input{Header: hdr("X-Session-Id", v), ConnectionID: "c1"})
		if got == v || got == strings.TrimSpace(v) && v != "" {
			t.Errorf("%q was used as an id", v)
		}
	}
	if got := s.Resolve(Input{Header: hdr("X-Session-Id", "  padded  ")}); got != "padded" {
		t.Fatalf("a candidate is trimmed: %q", got)
	}
	if got := s.Resolve(Input{Header: hdr("X-Session-Id", strings.Repeat("a", 256))}); len(got) != 256 {
		t.Fatalf("a 256 character id is allowed: %d", len(got))
	}
}

func TestAnInvalidFirstSourceFallsThroughToTheNext(t *testing.T) {
	s, _ := newStore()
	in := Input{Header: hdr("X-Session-Id", "bad id"), Body: []byte(`{"session_id":"good"}`)}
	if got := s.Resolve(in); got != "good" {
		t.Fatalf("got %q", got)
	}
}

func assistantBody(text string) []byte {
	return []byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"` + text + `"}]}`)
}

func TestTheOpeningAssistantTextNamesAConversation(t *testing.T) {
	s, _ := newStore()
	long := strings.Repeat("a", 60)
	a := s.Resolve(Input{Body: assistantBody(long), ConnectionID: "c1"})
	b := s.Resolve(Input{Body: assistantBody(long + " and more later"), ConnectionID: "c1"})
	if a != b {
		t.Fatal("the same opening text must give the same id")
	}
	other := s.Resolve(Input{Body: assistantBody(strings.Repeat("b", 60)), ConnectionID: "c1"})
	if other == a {
		t.Fatal("different opening text shared an id")
	}
	otherConn := s.Resolve(Input{Body: assistantBody(long), ConnectionID: "c2"})
	if otherConn == a {
		t.Fatal("the derived id must be scoped to the connection")
	}
}

func TestAShortAssistantTextDoesNotNameAConversation(t *testing.T) {
	s, _ := newStore()
	a := s.Resolve(Input{Body: assistantBody(strings.Repeat("a", 49)), ConnectionID: "c1"})
	b := s.Resolve(Input{Body: assistantBody(strings.Repeat("b", 49)), ConnectionID: "c1"})
	if a != b {
		t.Fatal("under 50 characters both fall back to the per-connection id")
	}
	exact := s.Resolve(Input{Body: assistantBody(strings.Repeat("a", 50)), ConnectionID: "c1"})
	if exact == a {
		t.Fatal("50 characters is enough to name a conversation")
	}
}

func TestResponsesInputItemsAreRead(t *testing.T) {
	s, _ := newStore()
	body := []byte(`{"input":[{"role":"assistant","content":[{"type":"output_text","text":"` + strings.Repeat("z", 60) + `"}]}]}`)
	a := s.Resolve(Input{Body: body, ConnectionID: "c"})
	b := s.Resolve(Input{Body: body, ConnectionID: "c"})
	c := s.Resolve(Input{ConnectionID: "c"})
	if a != b || a == c {
		t.Fatalf("%q %q %q", a, b, c)
	}
}

func TestOneMadeUpIDPerConnectionThatExpiresAfterTwoHours(t *testing.T) {
	s, clk := newStore()
	a := s.Resolve(Input{ConnectionID: "c1"})
	if a == "" || a != s.Resolve(Input{ConnectionID: "c1"}) {
		t.Fatal("the id must be stable for a connection")
	}
	if a == s.Resolve(Input{ConnectionID: "c2"}) {
		t.Fatal("two connections shared an id")
	}
	clk.advance(Life - time.Minute)
	if a != s.Resolve(Input{ConnectionID: "c1"}) {
		t.Fatal("an id used within its life must be kept")
	}
	clk.advance(Life - time.Minute)
	if a != s.Resolve(Input{ConnectionID: "c1"}) {
		t.Fatal("use refreshes the life")
	}
	clk.advance(Life + time.Second)
	if b := s.Resolve(Input{ConnectionID: "c1"}); b == a {
		t.Fatal("an id unused for over two hours must be replaced")
	}
}

func TestMadeUpIDsAreUnique(t *testing.T) {
	s, _ := newStore()
	seen := map[string]bool{}
	for i := range 200 {
		id := s.Resolve(Input{ConnectionID: strings.Repeat("c", 1) + string(rune('a'+i%26)) + strings.Repeat("x", i)})
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}

func TestTheTablesAreBounded(t *testing.T) {
	s, _ := newStore()
	for i := range maxMadeUp + 50 {
		s.Resolve(Input{ConnectionID: "conn-" + strconv.FormatInt(int64(i), 10)})
	}
	for i := range maxDerived + 50 {
		s.Resolve(Input{Body: assistantBody(strings.Repeat("a", 60) + strconv.FormatInt(int64(i), 10)), ConnectionID: "x"})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.madeUp.len() > maxMadeUp || s.derived.len() > maxDerived {
		t.Fatalf("tables grew to %d and %d", s.madeUp.len(), s.derived.len())
	}
}

func TestBoundIsKeyedPerConnectionAndPerProcess(t *testing.T) {
	a, _ := newStore()
	b, _ := newStore()
	v := a.Bound("client-chosen", "c1")
	if v == "client-chosen" || strings.Contains(v, "client-chosen") {
		t.Fatalf("the client's value went out as it was: %q", v)
	}
	if v != a.Bound("client-chosen", "c1") {
		t.Fatal("Bound must be stable within a process")
	}
	if v == a.Bound("client-chosen", "c2") {
		t.Fatal("two connections shared a bound value for one session id")
	}
	if v == a.Bound("other", "c1") {
		t.Fatal("two sessions shared a bound value")
	}
	if v == b.Bound("client-chosen", "c1") {
		t.Fatal("the key must differ per process")
	}
	if a.Bound("ab", "c") == a.Bound("a", "bc") {
		t.Fatal("the two inputs must not run together")
	}
}

func TestConcurrentUse(t *testing.T) {
	s, _ := newStore()
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 200 {
				s.Resolve(Input{ConnectionID: "c" + strconv.FormatInt(int64(j%7), 10), Body: assistantBody(strings.Repeat("a", 55) + strconv.FormatInt(int64(i), 10))})
				s.Bound("s", "c")
			}
		}()
	}
	wg.Wait()
}

func FuzzResolveSession(f *testing.F) {
	f.Add("X-Session-Id", "abc", `{"metadata":{"user_id":"u_session_aa"}}`, "c1")
	f.Add("X-Claude-Code-Session-Id", "\xff\x00", `{"messages":[{"role":"assistant","content":"`+strings.Repeat("a", 80)+`"}]}`, "")
	f.Add("Session_id", strings.Repeat("a", 300), `[`, "c")
	f.Fuzz(func(t *testing.T, name, value, body, conn string) {
		s, _ := newStore()
		h := http.Header{}
		if name != "" {
			h[http.CanonicalHeaderKey(name)] = []string{value}
		}
		id := s.Resolve(Input{Header: h, Body: []byte(body), ConnectionID: conn})
		if id == "" {
			t.Fatal("an empty id")
		}
		if len(id) > maxIDLength+len("antigravity:") && !strings.Contains(id, "-") {
			t.Fatalf("an over-long id: %d", len(id))
		}
		if strings.ContainsAny(id, " \r\n\t;,\"") {
			t.Fatalf("an id with a separator: %q", id)
		}
	})
}
