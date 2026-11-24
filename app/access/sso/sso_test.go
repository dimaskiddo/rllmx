package sso

import (
	"context"
	"errors"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rllmx/app/platform/network"
	"rllmx/app/platform/settings"
	"rllmx/tests"
)

func server(t *testing.T, url string) *settings.Server {
	t.Helper()
	values := tests.ServerValues()
	values["http.url"] = url
	s, err := settings.LoadServer(tests.Config{Values: values})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPublicBasePrefersAPPURL(t *testing.T) {
	r := &nethttp.Request{Host: "gw.example.com"}
	base, ok := PublicBase(server(t, "https://gw.example.com"), r, false)
	if !ok || base != "https://gw.example.com" {
		t.Fatalf("%q %v", base, ok)
	}
}

func TestPublicBaseUsesTheLoopbackHostOfALocalRequest(t *testing.T) {
	for _, host := range []string{"localhost:20128", "127.0.0.1:20128", "[::1]:20128"} {
		base, ok := PublicBase(server(t, ""), &nethttp.Request{Host: host}, true)
		if !ok || base != "http://"+host {
			t.Errorf("%q: %q %v", host, base, ok)
		}
	}
}

func TestPublicBaseRefusesARemoteRequestWithoutAPPURL(t *testing.T) {
	base, ok := PublicBase(server(t, ""), &nethttp.Request{Host: "gw.example.com"}, false)
	if ok || base != "" {
		t.Fatalf("%q %v: a remote caller must not choose the redirect URI", base, ok)
	}
}

func TestPublicBaseRefusesAForeignHostEvenLocally(t *testing.T) {
	for _, host := range []string{"gw.example.com", "localhost.evil.com", "0.0.0.0:20128", "", "a@b"} {
		if base, ok := PublicBase(server(t, ""), &nethttp.Request{Host: host}, true); ok {
			t.Errorf("%q became %q", host, base)
		}
	}
}

func TestCleanURLJoinsWithOneSlash(t *testing.T) {
	for _, tc := range []struct{ base, path, want string }{
		{"https://gw.example.com", "/login", "https://gw.example.com/login"},
		{"https://gw.example.com/", "/login", "https://gw.example.com/login"},
		{"https://gw.example.com", "login", "https://gw.example.com/login"},
	} {
		if got := CleanURL(tc.base, tc.path); got != tc.want {
			t.Errorf("%q+%q = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

func TestLoginURLUsesAFixedCode(t *testing.T) {
	got := LoginURL("https://gw.example.com", CodeNoAccount)
	if got != "https://gw.example.com/login?error=sso_no_account" {
		t.Fatalf("%q", got)
	}
}

func TestBindTiesTheStateToTheBrowser(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	mac := Bind(secret, "state-1")
	if mac == "" || mac == "state-1" {
		t.Fatalf("mac %q", mac)
	}
	if !BindMatches(secret, "state-1", mac) {
		t.Fatal("the bind it just made does not match")
	}
	if BindMatches(secret, "state-2", mac) {
		t.Fatal("another state matched")
	}
	if BindMatches([]byte("another-secret-another-secret-xx"), "state-1", mac) {
		t.Fatal("another secret matched")
	}
	if BindMatches(secret, "", "") || BindMatches(secret, "state-1", "") {
		t.Fatal("an empty value matched")
	}
}

func TestNoSecretBindsNothingAndMatchesNothing(t *testing.T) {
	if got := Bind(nil, "state"); got != "" {
		t.Fatalf("a bind under no secret is %q: anybody could compute it", got)
	}
	forged := BindFor([]byte("x"), BindCookie, "state")
	if BindMatches(nil, "state", forged) || BindMatches([]byte{}, "state", "") {
		t.Fatal("a callback matched with no secret")
	}
}

func TestWritePrivateStateIsSingleUse(t *testing.T) {
	p := NewPending()
	state, err := p.Put(State{Nonce: "n", Verifier: "v", Issuer: "i", ClientID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Take(state); !ok {
		t.Fatal("the first take failed")
	}
	if _, ok := p.Take(state); ok {
		t.Fatal("the state was taken twice")
	}
	if p.Len() != 0 {
		t.Fatalf("%d entries left", p.Len())
	}
}

func TestPendingStateExpires(t *testing.T) {
	p := NewPending()
	now := time.Now()
	p.Now = func() time.Time { return now }
	state, err := p.Put(State{Nonce: "n"})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(pendingLife + time.Second)
	if _, ok := p.Take(state); ok {
		t.Fatal("an expired sign-in was accepted")
	}
}

func TestPendingStateIsUnpredictable(t *testing.T) {
	p := NewPending()
	seen := map[string]bool{}
	for range 100 {
		state, err := p.Put(State{Nonce: "n"})
		if err != nil {
			t.Fatal(err)
		}
		if len(state) < 40 || seen[state] {
			t.Fatalf("state %q", state)
		}
		seen[state] = true
	}
}

func TestPendingRefusesWhenFull(t *testing.T) {
	p := NewPending()
	for range maxPending {
		if _, err := p.Put(State{Nonce: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Put(State{Nonce: "n"}); err == nil {
		t.Fatal("the store accepted an entry past its cap")
	}
}

func TestPendingDropsExpiredEntriesOnPut(t *testing.T) {
	p := NewPending()
	now := time.Now()
	p.Now = func() time.Time { return now }
	for range maxPending {
		if _, err := p.Put(State{Nonce: "n"}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(pendingLife + time.Second)
	if _, err := p.Put(State{Nonce: "n"}); err != nil {
		t.Fatalf("an expired store did not free a slot: %v", err)
	}
}

func TestOneOwnerCannotFillTheStore(t *testing.T) {
	p := NewPending()
	var first string
	for i := range maxPerOwner {
		state, err := p.Put(State{Nonce: "n", Owner: "attacker"})
		if err != nil {
			t.Fatalf("start %d refused: %v", i, err)
		}
		if i == 0 {
			first = state
		}
	}
	if _, err := p.Put(State{Nonce: "n", Owner: "attacker"}); err == nil {
		t.Fatal("one owner opened more flows than its share")
	}
	if _, err := p.Put(State{Nonce: "n", Owner: "someone-else"}); err != nil {
		t.Fatalf("another owner was refused while the first held its share: %v", err)
	}
	if _, ok := p.Take(first); !ok {
		t.Fatal("a held flow was lost")
	}
	if _, err := p.Put(State{Nonce: "n", Owner: "attacker"}); err != nil {
		t.Fatalf("finishing a flow did not give its slot back: %v", err)
	}
}

func TestOwnerSlotsReturnWhenFlowsExpire(t *testing.T) {
	p := NewPending()
	now := time.Now()
	p.Now = func() time.Time { return now }
	for range maxPerOwner {
		if _, err := p.Put(State{Nonce: "n", Owner: "a"}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(pendingLife + time.Second)
	if _, err := p.Put(State{Nonce: "n", Owner: "a"}); err != nil {
		t.Fatalf("expired flows still hold the owner's slots: %v", err)
	}
	if p.Len() != 1 || len(p.owners) != 1 || p.owners["a"] != 1 {
		t.Fatalf("entries %d owners %v", p.Len(), p.owners)
	}
}

func TestStartingAndFinishingFlowsDoesNotGrowTheQueue(t *testing.T) {
	p := NewPending()
	for range 5 * maxPending {
		state, err := p.Put(State{Nonce: "n", Owner: "a"})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := p.Take(state); !ok {
			t.Fatal("a fresh flow was not found")
		}
	}
	if len(p.queue) > 2*maxPending+1 {
		t.Fatalf("queue grew to %d", len(p.queue))
	}
}

func TestParseAdmins(t *testing.T) {
	none := ParseAdmins("")
	if none.IsAdmin("anyone@anywhere.test", false) {
		t.Fatal("an empty list named an admin")
	}
	list := ParseAdmins(" Ops@Example.com , @Corp.example , ")
	if !list.IsAdmin("ops@example.com", false) {
		t.Fatal("an exact entry was not the admin")
	}
	if !list.IsAdmin("someone@corp.example", false) {
		t.Fatal("a domain entry was not the admin")
	}
	if list.IsAdmin("someone@other.example", false) {
		t.Fatal("an address outside the list was made the admin")
	}
	if list.IsAdmin("someone@corp.example", true) {
		t.Fatal("a domain entry made a user-editable claim the admin")
	}
	if !list.IsAdmin("ops@example.com", true) {
		t.Fatal("an exact entry was refused for a user-editable claim")
	}
	if list.IsAdmin("", false) {
		t.Fatal("an empty address was made the admin")
	}
}

func TestTheClientRefusesAPrivateProviderWithoutAnEntry(t *testing.T) {
	c := NewClient(nil)
	var out map[string]any
	if err := c.GetJSON(context.Background(), "http://127.0.0.1:1/.well-known/openid-configuration", &out); err == nil {
		t.Fatal("an unlisted loopback provider was reached")
	}
}

func TestTheClientReportsNoProviderText(t *testing.T) {
	srv := newTestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal-detail-that-must-not-leak"}`))
	}))
	allow, err := network.ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	err = NewClient(allow).GetJSON(context.Background(), srv+"/x", &out)
	if err == nil {
		t.Fatal("a 500 was accepted")
	}
	if strings.Contains(err.Error(), "internal-detail") {
		t.Fatalf("the error repeats provider text: %v", err)
	}
}

func TestAnAnswerOverTheCapIsAnErrorAndOneAtTheCapIsRead(t *testing.T) {
	pad := func(n int) string { return `{"k":"` + strings.Repeat("x", n-8) + `"}` }
	srv := newTestServer(t, nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		if r.URL.Path == "/at" {
			_, _ = w.Write([]byte(pad(clientBodyCap)))
			return
		}
		_, _ = w.Write([]byte(pad(clientBodyCap + 1)))
	}))
	allow, err := network.ParseAllow("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	c := NewClient(allow)
	var out map[string]any
	if err := c.GetJSON(context.Background(), srv+"/at", &out); err != nil {
		t.Fatalf("an answer exactly at the cap was refused: %v", err)
	}
	if err := c.GetJSON(context.Background(), srv+"/over", &out); !errors.Is(err, ErrAnswer) {
		t.Fatalf("an answer one byte over the cap gave %v, want ErrAnswer", err)
	}
}

// newTestServer starts a loopback server the guard can reach when 127.0.0.1 is
// on the list, and returns its base URL.
func newTestServer(t *testing.T, h nethttp.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestShortHashNamesNoValue(t *testing.T) {
	got := ShortHash("https://idp.example.com")
	if strings.Contains(got, "idp.example") || len(got) != 12 {
		t.Fatalf("%q", got)
	}
	if ShortHash("a") == ShortHash("b") {
		t.Fatal("two values hash the same")
	}
}
