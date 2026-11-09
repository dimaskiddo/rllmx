package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/cache"
	"rllmx/app/repository"
	"rllmx/app/upstream"
	"rllmx/tests"
	"rllmx/tests/store"
)

// mintRig is a Minter over a real store and a fake mint endpoint that numbers
// the tokens it hands out.
type mintRig struct {
	m     *Minter
	st    *store.Store
	calls atomic.Int64
	log   *tests.Log
	now   time.Time
	mu    sync.Mutex
	// expires is the expiry each new token carries, as an offset from now.
	expires time.Duration
	status  int
	hold    chan struct{}
}

func newMintRig(t *testing.T) *mintRig {
	t.Helper()
	g := &mintRig{st: store.Migrated(t), log: &tests.Log{}, now: epoch, expires: time.Hour, status: 200}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := g.calls.Add(1)
		if g.hold != nil {
			<-g.hold
		}
		if r.Header.Get("Authorization") != "token GH-ACCESS" {
			w.WriteHeader(401)
			return
		}
		g.mu.Lock()
		exp, status := g.now.Add(g.expires).Unix(), g.status
		g.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"token":"MINTED-` + strconv.FormatInt(n, 10) + `","expires_at":` + strconv.FormatInt(exp, 10) + `}`))
	}))
	t.Cleanup(srv.Close)
	file := `{"id":"gh","category":"oauth","executor":"github","transport":{"baseUrl":"https://x.example/chat/completions","format":"openai"},"models":[],` +
		`"oauth":{"flow":"device","deviceUrl":"https://a.example/device","tokenUrl":"https://a.example/token","scope":"s","mint":"copilot",` +
		`"endpoints":{"copilotToken":"` + srv.URL + `"}}}`
	reg, err := upstream.Load(fstest.MapFS{"data/gh.json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	g.m = &Minter{
		Registry: reg, Tokens: &TokenClient{HTTP: srv.Client()}, Cache: cache.New(tests.NewCache()), Log: g.log,
		Store: func() PSDStore { return g.st.Connections() },
		Now:   func() time.Time { g.mu.Lock(); defer g.mu.Unlock(); return g.now },
	}
	return g
}

func (g *mintRig) advance(d time.Duration) { g.mu.Lock(); g.now = g.now.Add(d); g.mu.Unlock() }

func (g *mintRig) account(t *testing.T, fields map[string]string, psd string) repository.Connection {
	t.Helper()
	f := map[string]string{"accessToken": `"GH-ACCESS"`, "testStatus": `"active"`}
	for k, v := range fields {
		f[k] = v
	}
	c, err := g.st.Connections().Create(context.Background(), repository.ConnInput{Provider: "gh", AuthType: "oauth", Email: "a@x.test", Fields: f, PSD: psd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (g *mintRig) reload(t *testing.T, id string) repository.Connection {
	t.Helper()
	c, ok, err := g.st.Connections().Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("reload: %v %v", ok, err)
	}
	return c
}

func TestAFiftyRequestBurstMintsOnce(t *testing.T) {
	g := newMintRig(t)
	g.hold = make(chan struct{})
	c := g.account(t, nil, "")
	var wg sync.WaitGroup
	got := make(chan string, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tok, err := g.m.Token(context.Background(), c)
			if err != nil {
				t.Error(err)
			}
			got <- tok
		}()
	}
	time.Sleep(150 * time.Millisecond)
	close(g.hold)
	wg.Wait()
	close(got)
	if n := g.calls.Load(); n != 1 {
		t.Fatalf("%d mint calls for 50 requests", n)
	}
	for tok := range got {
		if tok != "MINTED-1" {
			t.Fatalf("token %q", tok)
		}
	}
}

func TestAStoredTokenIsUsedUntilFiveMinutesBeforeItEnds(t *testing.T) {
	g := newMintRig(t)
	c := g.account(t, nil, "")
	first, err := g.m.Token(context.Background(), c)
	if err != nil || first != "MINTED-1" {
		t.Fatalf("%q %v", first, err)
	}
	stored := g.reload(t, c.ID)
	if got, err := g.m.Token(context.Background(), stored); err != nil || got != "MINTED-1" || g.calls.Load() != 1 {
		t.Fatalf("a stored token was not used: %q %v, %d calls", got, err, g.calls.Load())
	}
	g.advance(time.Hour - 5*time.Minute - time.Second)
	if got, _ := g.m.Token(context.Background(), stored); got != "MINTED-1" || g.calls.Load() != 1 {
		t.Fatalf("minted before the lead time: %q, %d calls", got, g.calls.Load())
	}
	g.advance(2 * time.Second)
	// The 10 s result cache runs on the real clock, so a test that jumps the fake
	// one starts from an empty cache.
	g.m.Cache = cache.New(tests.NewCache())
	if got, _ := g.m.Token(context.Background(), stored); got != "MINTED-2" || g.calls.Load() != 2 {
		t.Fatalf("not renewed inside the lead time: %q, %d calls", got, g.calls.Load())
	}
}

func TestTheMintedTokenIsStoredSealedWithTheAccount(t *testing.T) {
	g := newMintRig(t)
	c := g.account(t, nil, `{"githubLogin":"octo"}`)
	if _, err := g.m.Token(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	psd, _ := g.reload(t, c.ID).Doc().GetRaw("providerSpecificData")
	var m map[string]any
	if err := json.Unmarshal([]byte(psd), &m); err != nil {
		t.Fatal(err)
	}
	if m["copilotToken"] != "MINTED-1" || m["githubLogin"] != "octo" || m["copilotTokenExpiresAt"] != float64(epoch.Add(time.Hour).Unix()) {
		t.Fatalf("%s", psd)
	}
	var rows []string
	if err := g.st.SQ.DB.Select(&rows, `SELECT "data" FROM "providerConnections" WHERE "id" = ?`, c.ID); err != nil || len(rows) != 1 {
		t.Fatalf("%v %d", err, len(rows))
	}
	stored := rows[0]
	if strings.Contains(stored, "MINTED-1") || !strings.Contains(stored, "enc:v1:") {
		t.Fatalf("the token is on disk in the clear: %s", stored)
	}
}

func TestAMintedTokenSurvivesARestart(t *testing.T) {
	g := newMintRig(t)
	c := g.account(t, nil, "")
	if _, err := g.m.Token(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	g.m.Cache = cache.New(tests.NewCache())
	if got, err := g.m.Token(context.Background(), g.reload(t, c.ID)); err != nil || got != "MINTED-1" || g.calls.Load() != 1 {
		t.Fatalf("%q %v, %d calls", got, err, g.calls.Load())
	}
}

func TestARefusedTokenIsReplacedOnceAndAnotherCallersRenewalIsUsed(t *testing.T) {
	g := newMintRig(t)
	c := g.account(t, nil, "")
	first, _ := g.m.Token(context.Background(), c)
	stored := g.reload(t, c.ID)
	g.advance(mintResult + time.Second)
	next, err := g.m.Renew(context.Background(), stored, first)
	if err != nil || next != "MINTED-2" || g.calls.Load() != 2 {
		t.Fatalf("%q %v, %d calls", next, err, g.calls.Load())
	}
	// A second request that was refused with the same old token finds the new one.
	again, err := g.m.Renew(context.Background(), g.reload(t, c.ID), first)
	if err != nil || again != "MINTED-2" || g.calls.Load() != 2 {
		t.Fatalf("%q %v, %d calls: a renewed token was renewed again", again, err, g.calls.Load())
	}
}

func TestAnotherCallersNewerTokenIsUsedWithoutAnyCall(t *testing.T) {
	g := newMintRig(t)
	c := g.account(t, nil, "")
	if _, err := g.m.Token(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	stored := g.reload(t, c.ID)
	// Even with the result cache empty, a request refused with an older token
	// finds the stored newer one and calls nothing.
	g.m.Cache = cache.New(tests.NewCache())
	got, err := g.m.Renew(context.Background(), stored, "an-older-token")
	if err != nil || got != "MINTED-1" || g.calls.Load() != 1 {
		t.Fatalf("%q %v, %d calls", got, err, g.calls.Load())
	}
	// A request refused with the stored token itself does mint.
	if got, _ := g.m.Renew(context.Background(), stored, "MINTED-1"); got != "MINTED-2" || g.calls.Load() != 2 {
		t.Fatalf("%q, %d calls", got, g.calls.Load())
	}
}

func TestOnlyPlainProfileFieldsAreStored(t *testing.T) {
	r := newDeviceRig(t, `{"access_token":"GH-ACCESS"}`)
	r.p.user = `{"login":"` + strings.Repeat("a", maxProfileField+1) + `","name":"Octo\u0000Cat","email":12,"id":"4242"}`
	s := r.start(t)
	r.advance(6 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.accounts.made[0]
	var psd map[string]string
	_ = json.Unmarshal([]byte(in.PSD), &psd)
	for _, k := range []string{"githubLogin", "githubName", "githubEmail", "githubUserId"} {
		if v, has := psd[k]; has {
			t.Errorf("%s = %q was stored", k, v)
		}
	}
	if in.Email != "" {
		t.Errorf("email %q", in.Email)
	}
}

func TestAMintFailureIsAClassAndCarriesNoToken(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		class  Class
	}{"client error": {403, ClassRejected}, "server error": {502, ClassTransport}} {
		g := newMintRig(t)
		g.status = tc.status
		c := g.account(t, nil, "")
		tok, err := g.m.Token(context.Background(), c)
		if tok != "" || ClassOf(err) != tc.class {
			t.Fatalf("%s: %q %v", name, tok, err)
		}
		if strings.Contains(err.Error(), "GH-ACCESS") || strings.Contains(strings.Join(g.log.Lines(), "\n"), "GH-ACCESS") {
			t.Fatalf("%s: the login token leaked", name)
		}
		if _, has := g.reload(t, c.ID).Doc().GetRaw("providerSpecificData"); has {
			t.Fatalf("%s: a failed mint wrote data", name)
		}
	}
}

func TestAnAnswerWithNoUsableTokenIsMalformed(t *testing.T) {
	for _, body := range []string{`{}`, `{"token":"t"}`, `{"token":"t","expires_at":0}`, `{"expires_at":99}`, `[]`, `x`} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		o := &upstream.OAuth{Endpoints: map[string]string{upstream.EndpointCopilotToken: srv.URL}}
		_, _, err := mint(context.Background(), &TokenClient{HTTP: srv.Client()}, o, "x")
		srv.Close()
		if ClassOf(err) != ClassMalformed {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func TestAnAccountWithoutALoginTokenOrOfAnotherProviderMintsNothing(t *testing.T) {
	g := newMintRig(t)
	c := g.account(t, map[string]string{"accessToken": `""`}, "")
	if _, err := g.m.Token(context.Background(), c); err != ErrNoToken {
		t.Fatalf("%v", err)
	}
	other := c
	other.Provider = "nothing"
	if _, err := g.m.Token(context.Background(), other); ClassOf(err) != ClassRejected {
		t.Fatalf("%v", err)
	}
	if g.calls.Load() != 0 {
		t.Fatal("a call was made")
	}
}

func TestAStoreThatIsNotOpenStillServesTheToken(t *testing.T) {
	g := newMintRig(t)
	g.m.Store = func() PSDStore { return nil }
	c := g.account(t, nil, "")
	if tok, err := g.m.Token(context.Background(), c); err != nil || tok != "MINTED-1" {
		t.Fatalf("%q %v", tok, err)
	}
}
