package oauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rllmx/app/llm/oauth"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rllmx/app/platform/cache"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

var epoch = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func docWith(t *testing.T, kv map[string]string) document.Doc {
	t.Helper()
	d, err := document.ParseDoc("{}")
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range kv {
		if err := d.SetString(k, v); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestWhenATokenIsDue(t *testing.T) {
	claude := flowFor(t, "claude")
	codex := flowFor(t, "codex")
	stamp := func(d time.Duration) string { return dialect.FormatTimestamp(epoch.Add(d)) }
	cases := []struct {
		name string
		flow oauth.Flow
		kv   map[string]string
		want bool
	}{
		{"claude far from expiry", claude, map[string]string{"refreshToken": "r", "expiresAt": stamp(5 * time.Hour)}, false},
		{"claude inside four hours", claude, map[string]string{"refreshToken": "r", "expiresAt": stamp(3 * time.Hour)}, true},
		{"claude already expired", claude, map[string]string{"refreshToken": "r", "expiresAt": stamp(-time.Minute)}, true},
		{"no refresh token is never due", claude, map[string]string{"expiresAt": stamp(-time.Hour)}, false},
		{"no expiry and no age rule", claude, map[string]string{"refreshToken": "r"}, false},
		{"unreadable expiry", claude, map[string]string{"refreshToken": "r", "expiresAt": "soon"}, false},
		{"codex inside ten minutes", codex, map[string]string{"refreshToken": "r", "expiresAt": stamp(9 * time.Minute), "lastRefreshAt": stamp(-time.Hour)}, true},
		{"codex outside ten minutes", codex, map[string]string{"refreshToken": "r", "expiresAt": stamp(11 * time.Minute), "lastRefreshAt": stamp(-time.Hour)}, false},
		{"codex last renewed eight days ago", codex, map[string]string{"refreshToken": "r", "expiresAt": stamp(48 * time.Hour), "lastRefreshAt": stamp(-8 * 24 * time.Hour)}, true},
		{"codex renewed seven days ago", codex, map[string]string{"refreshToken": "r", "expiresAt": stamp(48 * time.Hour), "lastRefreshAt": stamp(-7 * 24 * time.Hour)}, false},
		{"codex never recorded a renewal", codex, map[string]string{"refreshToken": "r", "expiresAt": stamp(48 * time.Hour)}, true},
	}
	for _, tc := range cases {
		if got := oauth.Due(tc.flow, docWith(t, tc.kv), epoch); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// tokenEndpoint is a TLS token endpoint that records each body and answers from
// a function.
type tokenEndpoint struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	types  []string
	calls  atomic.Int64
}

func newTokenEndpoint(t *testing.T, answer func(call int, body string) (int, string)) *tokenEndpoint {
	t.Helper()
	e := &tokenEndpoint{}
	e.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		body := string(raw[:n])
		call := int(e.calls.Add(1))
		e.mu.Lock()
		e.bodies = append(e.bodies, body)
		e.types = append(e.types, r.Header.Get("Content-Type"))
		e.mu.Unlock()
		status, out := answer(call, body)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(out))
	}))
	t.Cleanup(e.Close)
	return e
}

type renewRig struct {
	r     *oauth.Refresher
	st    *store.Store
	end   *tokenEndpoint
	log   *tests.Log
	waits []time.Duration
	now   time.Time
	mu    sync.Mutex
}

func newRenewRig(t *testing.T, answer func(call int, body string) (int, string)) *renewRig {
	t.Helper()
	rig := &renewRig{st: store.Migrated(t), log: &tests.Log{}, now: epoch}
	rig.end = newTokenEndpoint(t, answer)
	reg := fakeRegistry(t, rig.end.URL)
	rig.r = &oauth.Refresher{
		Flow: func(provider string) (oauth.Flow, bool) {
			id, ok := reg.Resolve(provider)
			if !ok {
				return oauth.Flow{}, false
			}
			e, _ := reg.ByID(id)
			f, err := oauth.NewFlow(e, "client-"+id)
			return f, err == nil
		},
		Tokens: &oauth.TokenClient{HTTP: rig.end.Client()},
		Cache:  cache.New(tests.NewCache()),
		Store:  func() oauth.Store { return rig.st.Connections() },
		Log:    rig.log,
		Now:    func() time.Time { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.now },
		Wait: func(_ context.Context, d time.Duration) error {
			rig.mu.Lock()
			rig.waits = append(rig.waits, d)
			rig.mu.Unlock()
			return nil
		},
	}
	return rig
}

func (g *renewRig) conn(t *testing.T, provider, email string, kv map[string]string) repository.Connection {
	t.Helper()
	fields := map[string]string{}
	for k, v := range kv {
		b, _ := json.Marshal(v)
		fields[k] = string(b)
	}
	c, err := g.st.Connections().Create(context.Background(), repository.ConnInput{Provider: provider, AuthType: "oauth", Email: email, Fields: fields}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (g *renewRig) reload(t *testing.T, id string) document.Doc {
	t.Helper()
	c, ok, err := g.st.Connections().Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("reload: %v %v", ok, err)
	}
	return c.Doc()
}

func good(call int, _ string) (int, string) {
	return 200, `{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":3600,"id_token":"NEW-ID"}`
}

func dueClaude(g *renewRig, t *testing.T) repository.Connection {
	return g.conn(t, "claude", "", map[string]string{
		"accessToken": "OLD-AT", "refreshToken": "OLD-RT", "testStatus": "active",
		"expiresAt": dialect.FormatTimestamp(epoch.Add(time.Minute)),
	})
}

func TestADueTokenIsRenewedWithTheRightBody(t *testing.T) {
	for _, tc := range []struct{ provider, client string }{{"claude", "client-claude"}, {"codex", "client-codex"}} {
		g := newRenewRig(t, good)
		c := g.conn(t, tc.provider, tc.provider+"@x.test", map[string]string{
			"accessToken": "OLD-AT", "refreshToken": "OLD-RT", "testStatus": "active",
			"expiresAt": dialect.FormatTimestamp(epoch.Add(time.Minute)),
		})
		got, err := g.r.Ready(context.Background(), c)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if at, _ := got.Doc().GetString("accessToken"); at != "NEW-AT" {
			t.Fatalf("%s: access token %q", tc.provider, at)
		}
		if g.end.types[0] != "application/json" {
			t.Errorf("%s: content type %s", tc.provider, g.end.types[0])
		}
		var body map[string]string
		if err := json.Unmarshal([]byte(g.end.bodies[0]), &body); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{"grant_type": "refresh_token", "refresh_token": "OLD-RT", "client_id": tc.client}
		if len(body) != len(want) {
			t.Errorf("%s: body %v carries more than %v (a Codex refresh sends no scope)", tc.provider, body, want)
		}
		for k, v := range want {
			if body[k] != v {
				t.Errorf("%s: %s = %q, want %q", tc.provider, k, body[k], v)
			}
		}
		d := g.reload(t, c.ID)
		for k, v := range map[string]string{"accessToken": "NEW-AT", "refreshToken": "NEW-RT", "idToken": "NEW-ID", "lastRefreshAt": dialect.FormatTimestamp(epoch), "expiresAt": dialect.FormatTimestamp(epoch.Add(time.Hour))} {
			if got, _ := d.GetString(k); got != v {
				t.Errorf("%s: stored %s = %q, want %q", tc.provider, k, got, v)
			}
		}
	}
}

func TestATokenThatIsNotDueIsLeftAlone(t *testing.T) {
	g := newRenewRig(t, good)
	c := g.conn(t, "claude", "", map[string]string{
		"accessToken": "AT", "refreshToken": "RT", "expiresAt": dialect.FormatTimestamp(epoch.Add(10 * time.Hour)),
	})
	got, err := g.r.Ready(context.Background(), c)
	if err != nil || got.Data != c.Data || g.end.calls.Load() != 0 {
		t.Fatalf("%v, %d calls", err, g.end.calls.Load())
	}
}

func TestAnAPIKeyAccountIsNeverRenewed(t *testing.T) {
	g := newRenewRig(t, good)
	c, err := g.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "claude", AuthType: "apikey", Name: "k", Fields: map[string]string{"apiKey": `"sk"`, "refreshToken": `"RT"`, "expiresAt": `"2020-01-01T00:00:00.000Z"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.r.Ready(context.Background(), c); err != nil || g.end.calls.Load() != 0 {
		t.Fatalf("%v, %d calls", err, g.end.calls.Load())
	}
	if _, err := g.r.Renew(context.Background(), c); err != oauth.ErrNoRefresh || g.end.calls.Load() != 0 {
		t.Fatalf("renew: %v, %d calls", err, g.end.calls.Load())
	}
}

func TestTheRefreshTokenStaysWhenTheAnswerOmitsIt(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 200, `{"access_token":"NEW-AT","expires_in":60}` })
	c := dueClaude(g, t)
	if _, err := g.r.Ready(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if rt, _ := g.reload(t, c.ID).GetString("refreshToken"); rt != "OLD-RT" {
		t.Fatalf("refresh token = %q", rt)
	}
}

func TestARenewalNeverEndsACooldown(t *testing.T) {
	g := newRenewRig(t, good)
	c := g.conn(t, "claude", "", map[string]string{
		"accessToken": "OLD-AT", "refreshToken": "OLD-RT", "testStatus": "unavailable",
		"expiresAt": dialect.FormatTimestamp(epoch.Add(time.Minute)),
	})
	if _, err := g.r.Ready(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.reload(t, c.ID).GetString("testStatus"); st != "unavailable" {
		t.Fatalf("status = %q: a renewal hid a real cooldown", st)
	}
}

func TestTwentyConcurrentCallersMakeOneTokenCall(t *testing.T) {
	g := newRenewRig(t, func(call int, body string) (int, string) {
		time.Sleep(50 * time.Millisecond)
		return good(call, body)
	})
	c := dueClaude(g, t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := g.r.Ready(context.Background(), c)
			if err != nil {
				t.Errorf("ready: %v", err)
				return
			}
			if at, _ := got.Doc().GetString("accessToken"); at != "NEW-AT" {
				t.Errorf("a caller saw %q", at)
			}
		}()
	}
	wg.Wait()
	if n := g.end.calls.Load(); n != 1 {
		t.Fatalf("%d token calls, want 1", n)
	}
}

func TestATokenAnotherCallerAlreadyRenewedMakesNoCall(t *testing.T) {
	g := newRenewRig(t, good)
	c := dueClaude(g, t)
	if _, _, err := g.st.Connections().SwapCredentials(context.Background(), c.ID, "OLD-RT",
		map[string]string{"accessToken": `"OTHER-AT"`, "refreshToken": `"OTHER-RT"`}); err != nil {
		t.Fatal(err)
	}
	got, err := g.r.Renew(context.Background(), c)
	if err != nil || g.end.calls.Load() != 0 {
		t.Fatalf("%v, %d calls", err, g.end.calls.Load())
	}
	if at, _ := got.Doc().GetString("accessToken"); at != "OTHER-AT" {
		t.Fatalf("the stored token did not come back: %q", at)
	}
}

func TestADeadRefreshTokenMarksTheAccountOnceAndIsNeverSpentAgain(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) {
		return 400, `{"error":"invalid_grant","error_description":"SECRET-PROVIDER-TEXT"}`
	})
	c := dueClaude(g, t)
	_, err := g.r.Ready(context.Background(), c)
	if err != oauth.ErrExpired {
		t.Fatalf("err = %v", err)
	}
	d := g.reload(t, c.ID)
	if st, _ := d.GetString("testStatus"); st != repository.StatusExpired {
		t.Fatalf("status = %q", st)
	}
	if msg, _ := d.GetString("lastError"); msg != oauth.ReconnectMessage {
		t.Fatalf("lastError = %q", msg)
	}
	for _, line := range g.log.Lines() {
		if strings.Contains(line, "SECRET") {
			t.Fatalf("provider text reached the log: %s", line)
		}
	}
	for range 3 {
		if _, err := g.r.Renew(context.Background(), c); err != oauth.ErrExpired {
			t.Fatalf("renew: %v", err)
		}
	}
	if n := g.end.calls.Load(); n != 1 {
		t.Fatalf("%d token calls after the account was marked, want 1", n)
	}
}

func TestATransportFailureMarksNothing(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 503, `<html>down</html>` })
	c := dueClaude(g, t)
	if _, err := g.r.Ready(context.Background(), c); oauth.ClassOf(err) != oauth.ClassRejected {
		t.Fatalf("err = %v", err)
	}
	if st, _ := g.reload(t, c.ID).GetString("testStatus"); st != "active" {
		t.Fatalf("a provider outage marked the account: %q", st)
	}
}

func TestADeadTokenIsNotMarkedWhenAnotherCallerAlreadyRotatedIt(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 400, `{"error":"invalid_grant"}` })
	c := dueClaude(g, t)
	g.st.Connections().SwapCredentials(context.Background(), c.ID, "OLD-RT", map[string]string{"refreshToken": `"ROTATED"`, "accessToken": `"A2"`})
	st, _ := g.r.Cooldowns()
	marked, err := st.MarkExpired(context.Background(), c.ID, "OLD-RT", oauth.ReconnectMessage, epoch)
	if err != nil || marked {
		t.Fatalf("a rotated token was declared dead: %v %v", marked, err)
	}
}

func TestRenewTriesThreeTimesWithGrowingPauses(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 502, `bad gateway` })
	c := dueClaude(g, t)
	_, err := g.r.Renew(context.Background(), c)
	if oauth.ClassOf(err) != oauth.ClassRejected {
		t.Fatalf("err = %v", err)
	}
	if n := g.end.calls.Load(); n != 3 {
		t.Fatalf("%d calls, want 3", n)
	}
	if len(g.waits) != 2 || g.waits[0] != time.Second || g.waits[1] != 2*time.Second {
		t.Fatalf("pauses = %v", g.waits)
	}
}

func TestRenewStopsAtOnceOnADeadToken(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 400, `{"error":"invalid_grant"}` })
	c := dueClaude(g, t)
	if _, err := g.r.Renew(context.Background(), c); err != oauth.ErrExpired {
		t.Fatalf("err = %v", err)
	}
	if g.end.calls.Load() != 1 || len(g.waits) != 0 {
		t.Fatalf("%d calls, pauses %v", g.end.calls.Load(), g.waits)
	}
}

func TestRenewSucceedsOnALaterTry(t *testing.T) {
	g := newRenewRig(t, func(call int, body string) (int, string) {
		if call < 3 {
			return 500, `oops`
		}
		return good(call, body)
	})
	c := dueClaude(g, t)
	got, err := g.r.Renew(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if at, _ := got.Doc().GetString("accessToken"); at != "NEW-AT" {
		t.Fatalf("token %q", at)
	}
}

func TestEnsure(t *testing.T) {
	g := newRenewRig(t, good)
	ctx := context.Background()
	stamp := func(d time.Duration) string { return dialect.FormatTimestamp(epoch.Add(d)) }

	fresh := g.conn(t, "claude", "", map[string]string{"accessToken": "AT", "refreshToken": "RT", "expiresAt": stamp(10 * time.Hour)})
	if refreshed, err := g.r.Ensure(ctx, fresh.ID); err != nil || refreshed {
		t.Fatalf("fresh: %v %v", refreshed, err)
	}
	if g.end.calls.Load() != 0 {
		t.Fatal("a fresh token was renewed by a test")
	}

	due := dueClaude(g, t)
	if refreshed, err := g.r.Ensure(ctx, due.ID); err != nil || !refreshed {
		t.Fatalf("due: %v %v", refreshed, err)
	}

	none := g.conn(t, "claude", "", map[string]string{"refreshToken": "RT"})
	if _, err := g.r.Ensure(ctx, none.ID); err != oauth.ErrNoToken {
		t.Fatalf("no access token: %v", err)
	}

	lapsed := g.conn(t, "claude", "", map[string]string{"accessToken": "AT", "expiresAt": stamp(-time.Hour)})
	if _, err := g.r.Ensure(ctx, lapsed.ID); err != oauth.ErrNoRefresh {
		t.Fatalf("lapsed with nothing to renew with: %v", err)
	}

	ok := g.conn(t, "claude", "", map[string]string{"accessToken": "AT", "expiresAt": stamp(time.Hour)})
	if refreshed, err := g.r.Ensure(ctx, ok.ID); err != nil || refreshed {
		t.Fatalf("held and unexpired: %v %v", refreshed, err)
	}
}

func TestEnsureDoesNotTryADeadAccountAgain(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 400, `{"error":"invalid_grant"}` })
	c := dueClaude(g, t)
	if _, err := g.r.Ensure(context.Background(), c.ID); err != oauth.ErrExpired {
		t.Fatalf("first: %v", err)
	}
	if _, err := g.r.Ensure(context.Background(), c.ID); err != oauth.ErrExpired {
		t.Fatalf("second: %v", err)
	}
	if g.end.calls.Load() != 1 {
		t.Fatalf("%d calls", g.end.calls.Load())
	}
}

func TestRefreshWithoutACacheStillWorks(t *testing.T) {
	g := newRenewRig(t, good)
	g.r.Cache = nil
	c := dueClaude(g, t)
	if _, err := g.r.Ready(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshBeforeTheStoreOpensIsAStorageError(t *testing.T) {
	g := newRenewRig(t, good)
	g.r.Store = func() oauth.Store { return nil }
	if _, err := g.r.Refresh(context.Background(), "x", ""); err != oauth.ErrStorage {
		t.Fatalf("%v", err)
	}
	if n := g.r.Pass(context.Background(), 0); n != 0 {
		t.Fatalf("pass renewed %d", n)
	}
}

func TestThePassPicksTheRightAccounts(t *testing.T) {
	g := newRenewRig(t, good)
	stamp := func(d time.Duration) string { return dialect.FormatTimestamp(epoch.Add(d)) }
	mk := func(provider string, kv map[string]string) string {
		return g.conn(t, provider, kv["email"], kv).ID
	}
	soon := mk("claude", map[string]string{"accessToken": "a", "refreshToken": "r1", "expiresAt": stamp(3 * time.Hour)})
	farClaude := mk("claude", map[string]string{"accessToken": "a", "refreshToken": "r2", "expiresAt": stamp(5 * time.Hour)})
	codexSoon := mk("codex", map[string]string{"email": "c@x.test", "accessToken": "a", "refreshToken": "r3", "expiresAt": stamp(20 * time.Minute)})
	codexFar := mk("codex", map[string]string{"email": "d@x.test", "accessToken": "a", "refreshToken": "r4", "expiresAt": stamp(40 * time.Minute)})
	dead := mk("claude", map[string]string{"accessToken": "a", "refreshToken": "r5", "expiresAt": stamp(time.Minute), "testStatus": repository.StatusExpired})
	noToken := mk("claude", map[string]string{"accessToken": "a", "expiresAt": stamp(time.Minute)})
	noExpiry := mk("claude", map[string]string{"accessToken": "a", "refreshToken": "r6"})

	if n := g.r.Pass(context.Background(), 1500*time.Millisecond); n != 2 {
		t.Fatalf("renewed %d, want 2 (a long Claude lead and a Codex token inside half an hour); log: %v", n, g.log.Lines())
	}
	changed := func(id string) bool {
		at, _ := g.reload(t, id).GetString("accessToken")
		return at == "NEW-AT"
	}
	for id, want := range map[string]bool{soon: true, codexSoon: true, farClaude: false, codexFar: false, dead: false, noToken: false, noExpiry: false} {
		if changed(id) != want {
			t.Errorf("account %s: renewed = %v, want %v", id[:8], changed(id), want)
		}
	}
	if len(g.waits) != 1 || g.waits[0] != 1500*time.Millisecond {
		t.Fatalf("pauses between accounts = %v", g.waits)
	}
}

func TestOneFailureDoesNotStopThePass(t *testing.T) {
	g := newRenewRig(t, func(call int, body string) (int, string) {
		if call == 1 {
			return 500, `down`
		}
		return good(call, body)
	})
	a := dueClaude(g, t)
	b := g.conn(t, "claude", "", map[string]string{"accessToken": "OLD-AT2", "refreshToken": "OLD-RT2", "expiresAt": dialect.FormatTimestamp(epoch.Add(time.Minute))})
	if n := g.r.Pass(context.Background(), 0); n != 1 {
		t.Fatalf("renewed %d, want 1", n)
	}
	got := 0
	for _, id := range []string{a.ID, b.ID} {
		if at, _ := g.reload(t, id).GetString("accessToken"); at == "NEW-AT" {
			got++
		}
	}
	if got != 1 {
		t.Fatalf("%d accounts renewed", got)
	}
	for _, line := range g.log.Lines() {
		if strings.Contains(line, "OLD-RT") || strings.Contains(line, "down") {
			t.Fatalf("the log carries a secret or provider text: %s", line)
		}
	}
}

func TestThePassStopsWhenCancelled(t *testing.T) {
	g := newRenewRig(t, good)
	dueClaude(g, t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if n := g.r.Pass(ctx, 0); n != 0 {
		t.Fatalf("renewed %d after cancel", n)
	}
}

func TestTheLoopWaitsTenSecondsThenFiveMinutes(t *testing.T) {
	g := newRenewRig(t, good)
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var seen []time.Duration
	g.r.Wait = func(c context.Context, d time.Duration) error {
		mu.Lock()
		seen = append(seen, d)
		n := len(seen)
		mu.Unlock()
		if n >= 3 {
			cancel()
			return c.Err()
		}
		return nil
	}
	g.r.Loop(ctx, 0)
	if len(seen) != 3 || seen[0] != oauth.FirstPass || seen[1] != oauth.PassEvery || seen[2] != oauth.PassEvery {
		t.Fatalf("waits = %v", seen)
	}
}

func TestAMarkedAccountIsNeverCalledValidEvenWhenItsTokenIsNotDue(t *testing.T) {
	g := newRenewRig(t, good)
	c := g.conn(t, "claude", "", map[string]string{
		"accessToken": "AT", "refreshToken": "RT", "testStatus": repository.StatusExpired,
		"expiresAt": dialect.FormatTimestamp(epoch.Add(10 * time.Hour)),
	})
	if _, err := g.r.Ensure(context.Background(), c.ID); err != oauth.ErrExpired {
		t.Fatalf("Ensure = %v", err)
	}
	if _, err := g.r.Refresh(context.Background(), c.ID, ""); err != oauth.ErrExpired {
		t.Fatalf("Refresh = %v", err)
	}
	if g.end.calls.Load() != 0 {
		t.Fatalf("%d token calls for a dead account", g.end.calls.Load())
	}
}

func TestAnAccountOfASlowProviderIsFollowedByTheLongerPauseWithJitter(t *testing.T) {
	for name, tc := range map[string]struct {
		first, second string
		want          time.Duration
	}{
		"slow then common": {"claude", "codex", 15 * time.Second},
		"common then slow": {"codex", "claude", 1500 * time.Millisecond},
	} {
		g := newRenewRig(t, good)
		g.r.SlowGap = 12 * time.Second
		g.r.Jitter = func(max time.Duration) time.Duration {
			if max != oauth.MaxSlowJitter {
				t.Errorf("jitter bound %v", max)
			}
			return 3 * time.Second
		}
		slow := g.r.Flow
		g.r.Flow = func(provider string) (oauth.Flow, bool) {
			f, ok := slow(provider)
			f.SlowRefresh = provider == "claude"
			return f, ok
		}
		soon := dialect.FormatTimestamp(epoch.Add(time.Minute))
		g.conn(t, tc.first, "a@x.test", map[string]string{"accessToken": "a", "refreshToken": "r1", "expiresAt": soon, "email": "a@x.test"})
		g.conn(t, tc.second, "b@x.test", map[string]string{"accessToken": "a", "refreshToken": "r2", "expiresAt": soon, "email": "b@x.test"})
		if n := g.r.Pass(context.Background(), 1500*time.Millisecond); n != 2 {
			t.Fatalf("%s: renewed %d", name, n)
		}
		if len(g.waits) != 1 || g.waits[0] != tc.want {
			t.Errorf("%s: pauses %v, want one of %v", name, g.waits, tc.want)
		}
	}
}

func TestTheCommonGapIsKeptWhenNoSlowGapIsSet(t *testing.T) {
	g := newRenewRig(t, good)
	slow := g.r.Flow
	g.r.Flow = func(provider string) (oauth.Flow, bool) {
		f, ok := slow(provider)
		f.SlowRefresh = true
		return f, ok
	}
	at := map[string]string{"accessToken": "a", "refreshToken": "r1", "expiresAt": dialect.FormatTimestamp(epoch.Add(time.Minute))}
	g.conn(t, "claude", "", at)
	g.conn(t, "claude", "", map[string]string{"accessToken": "a", "refreshToken": "r2", "expiresAt": at["expiresAt"]})
	g.r.Pass(context.Background(), 1500*time.Millisecond)
	if len(g.waits) != 1 || g.waits[0] != 1500*time.Millisecond {
		t.Fatalf("%v", g.waits)
	}
}

func TestAnAccountTheProviderKeepsRejectingIsMarkedExpired(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 400, `{"error":"nope"}` })
	c := dueClaude(g, t)
	for pass := 1; pass <= 2; pass++ {
		g.r.Pass(context.Background(), 0)
		if status, _ := g.reload(t, c.ID).GetString("testStatus"); status == repository.StatusExpired {
			t.Fatalf("expired after %d rejected passes", pass)
		}
	}
	g.r.Pass(context.Background(), 0)
	d := g.reload(t, c.ID)
	if status, _ := d.GetString("testStatus"); status != repository.StatusExpired {
		t.Fatalf("still %q after three rejected passes", status)
	}
	if msg, _ := d.GetString("lastError"); msg != oauth.ReconnectMessage {
		t.Fatalf("lastError %q", msg)
	}
	calls := g.end.calls.Load()
	g.r.Pass(context.Background(), 0)
	if g.end.calls.Load() != calls {
		t.Fatal("the pass kept calling the provider for an expired account")
	}
}

func TestARenewalThatSucceedsClearsTheRejectCount(t *testing.T) {
	// Two rejections, a success, two more rejections: four in all, never three in a row.
	g := newRenewRig(t, func(call int, body string) (int, string) {
		if call == 3 {
			return good(call, body)
		}
		return 400, `{"error":"nope"}`
	})
	c := dueClaude(g, t)
	for range 5 {
		g.r.Pass(context.Background(), 0)
	}
	if status, _ := g.reload(t, c.ID).GetString("testStatus"); status == repository.StatusExpired {
		t.Fatal("a success did not clear the count")
	}
}

func TestATransportFailureNeverExpiresAnAccount(t *testing.T) {
	g := newRenewRig(t, func(int, string) (int, string) { return 500, `down` })
	c := dueClaude(g, t)
	for range 5 {
		g.r.Pass(context.Background(), 0)
	}
	if status, _ := g.reload(t, c.ID).GetString("testStatus"); status == repository.StatusExpired {
		t.Fatal("an outage expired the account")
	}
}
