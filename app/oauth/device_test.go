package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/upstream"
)

const deviceSecret = "DEVICE-CODE-SECRET"

// provider is a TLS fake of a device login provider and the two GitHub calls the
// login makes. polls holds what each token call answers, in order; the last one
// repeats.
type provider struct {
	*httptest.Server
	mu       sync.Mutex
	polls    []string
	seen     []string
	device   string
	user     string
	mintBody string
	mintCode int
	tokens   atomic.Int64
	mints    atomic.Int64
	pollWait chan struct{}
}

func newProvider(t *testing.T, polls ...string) *provider {
	t.Helper()
	p := &provider{
		polls:    polls,
		device:   `{"device_code":"` + deviceSecret + `","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":900,"interval":5}`,
		user:     `{"login":"octo","name":"Octo Cat","email":"o@x.test","id":4242}`,
		mintBody: `{"token":"tid=COPILOT-1","expires_at":4102444800}`,
		mintCode: 200,
	}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		p.mu.Lock()
		p.seen = append(p.seen, r.Method+" "+r.URL.Path+" "+string(raw[:n]))
		p.mu.Unlock()
		switch r.URL.Path {
		case "/device":
			_, _ = w.Write([]byte(p.device))
		case "/token":
			i := int(p.tokens.Add(1)) - 1
			if p.pollWait != nil {
				<-p.pollWait
			}
			out := p.polls[min(i, len(p.polls)-1)]
			_, _ = w.Write([]byte(out))
		case "/user":
			_, _ = w.Write([]byte(p.user))
		case "/copilot":
			p.mints.Add(1)
			if r.Header.Get("Authorization") != "token GH-ACCESS" {
				w.WriteHeader(401)
				return
			}
			w.WriteHeader(p.mintCode)
			_, _ = w.Write([]byte(p.mintBody))
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *provider) registry(t *testing.T) *upstream.Registry {
	t.Helper()
	file := `{"id":"gh","category":"oauth","executor":"github","transport":{"baseUrl":"https://x.example/chat/completions","format":"openai"},"models":[],` +
		`"oauth":{"flow":"device","deviceUrl":"` + p.URL + `/device","tokenUrl":"` + p.URL + `/token","scope":"read:user","hook":"github","mint":"copilot",` +
		`"endpoints":{"copilotToken":"` + p.URL + `/copilot","user":"` + p.URL + `/user"},` +
		`"headers":{"user-agent":"UA/1","editor-version":"ed/1","copilot-integration-id":"not-for-the-mint"}}}`
	reg, err := upstream.Load(fstest.MapFS{"data/gh.json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

type deviceRig struct {
	svc      *Service
	accounts *fakeAccounts
	p        *provider
	now      time.Time
	mu       sync.Mutex
}

func newDeviceRig(t *testing.T, polls ...string) *deviceRig {
	t.Helper()
	p := newProvider(t, polls...)
	r := &deviceRig{p: p, accounts: &fakeAccounts{}, now: time.Unix(1_700_000_000, 0)}
	clock := func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	sessions := NewSessions()
	sessions.Now = clock
	r.svc = &Service{
		Registry: p.registry(t), ClientID: func(string) string { return "client-gh" }, Sessions: sessions,
		Tokens: &TokenClient{HTTP: p.Client()}, Now: clock, Accounts: func() Accounts { return r.accounts },
	}
	return r
}

func (r *deviceRig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

func (r *deviceRig) start(t *testing.T) DeviceStarted {
	t.Helper()
	s, err := r.svc.StartDevice(context.Background(), "gh", "bind")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStartingADeviceLoginShowsTheUserCodeAndKeepsTheDeviceCode(t *testing.T) {
	r := newDeviceRig(t, `{"error":"authorization_pending"}`)
	s := r.start(t)
	if s.UserCode != "ABCD-1234" || s.VerificationURI != "https://github.com/login/device" || s.ExpiresIn != 900 || s.Interval != 5 || len(s.State) < 40 {
		t.Fatalf("%+v", s)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), deviceSecret) {
		t.Fatalf("the device code reached the browser: %s", raw)
	}
	if len(r.p.seen) != 1 || !strings.Contains(r.p.seen[0], "client_id=client-gh") || !strings.Contains(r.p.seen[0], "scope=read%3Auser") {
		t.Fatalf("start call: %v", r.p.seen)
	}
}

func TestAPollBeforeTheIntervalMakesNoProviderCall(t *testing.T) {
	r := newDeviceRig(t, `{"error":"authorization_pending"}`)
	s := r.start(t)
	for range 5 {
		if got := r.svc.PollDevice(context.Background(), s.State, "bind"); got.Status != "pending" {
			t.Fatalf("%+v", got)
		}
	}
	if r.p.tokens.Load() != 0 {
		t.Fatalf("%d provider calls inside the interval", r.p.tokens.Load())
	}
	r.advance(5 * time.Second)
	r.svc.PollDevice(context.Background(), s.State, "bind")
	r.svc.PollDevice(context.Background(), s.State, "bind")
	if r.p.tokens.Load() != 1 {
		t.Fatalf("%d provider calls for one interval", r.p.tokens.Load())
	}
}

func TestSlowDownLengthensThePauseUpToThirtySeconds(t *testing.T) {
	r := newDeviceRig(t, `{"error":"slow_down"}`)
	s := r.start(t)
	var want = []int{10, 15, 20, 25, 30, 30}
	var got []int
	for range want {
		r.advance(31 * time.Second)
		p := r.svc.PollDevice(context.Background(), s.State, "bind")
		if p.Status != "pending" {
			t.Fatalf("%+v", p)
		}
		got = append(got, p.Interval)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("pauses %v, want %v", got, want)
	}
}

func TestAnApprovedDeviceLoginStoresTheAccountWithItsCopilotToken(t *testing.T) {
	r := newDeviceRig(t, `{"error":"authorization_pending"}`, `{"access_token":"GH-ACCESS","token_type":"bearer","scope":"read:user"}`)
	s := r.start(t)
	r.advance(6 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "pending" {
		t.Fatalf("%+v", p)
	}
	r.advance(6 * time.Second)
	p := r.svc.PollDevice(context.Background(), s.State, "bind")
	if p.Status != "done" || p.ConnectionID == "" {
		t.Fatalf("%+v", p)
	}
	if len(r.accounts.made) != 1 {
		t.Fatalf("%d accounts", len(r.accounts.made))
	}
	in := r.accounts.made[0]
	if in.Provider != "gh" || in.AuthType != "oauth" || in.Email != "o@x.test" || in.Fields["accessToken"] != `"GH-ACCESS"` {
		t.Fatalf("%+v", in)
	}
	if _, has := in.Fields["refreshToken"]; has {
		t.Fatal("a refresh token was stored that the provider never sent")
	}
	var psd map[string]string
	if err := json.Unmarshal([]byte(in.PSD), &psd); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"copilotToken": "tid=COPILOT-1", "copilotTokenExpiresAt": "4102444800",
		"githubLogin": "octo", "githubName": "Octo Cat", "githubEmail": "o@x.test", "githubUserId": "4242",
	}
	for k, v := range want {
		if psd[k] != v {
			t.Errorf("%s = %q, want %q", k, psd[k], v)
		}
	}
	// The outcome is read once, like the redirect flows.
	if again := r.svc.PollDevice(context.Background(), s.State, "bind"); again.Status != "done" {
		t.Fatalf("outcome: %+v", again)
	}
	if again := r.svc.PollDevice(context.Background(), s.State, "bind"); again.Status != "unknown" {
		t.Fatalf("read twice: %+v", again)
	}
}

func TestALoginStillConnectsWhenTheMintAndTheProfileFail(t *testing.T) {
	r := newDeviceRig(t, `{"access_token":"GH-ACCESS"}`)
	r.p.mintCode, r.p.user = 500, `not json`
	s := r.start(t)
	r.advance(6 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if in := r.accounts.made[0]; in.PSD != "" || in.Email != "" {
		t.Fatalf("data from failed calls: %+v", in)
	}
}

func TestADeviceLoginEndsWithAFixedWordAndStoresNothing(t *testing.T) {
	for name, tc := range map[string]struct{ body, code string }{
		"expired":  {`{"error":"expired_token","error_description":"SECRET-TEXT"}`, CodeExpired},
		"denied":   {`{"error":"access_denied","error_description":"SECRET-TEXT"}`, CodeDenied},
		"other":    {`{"error":"incorrect_client_credentials","error_description":"SECRET-TEXT"}`, CodeRefused},
		"no token": {`{"token_type":"bearer"}`, CodeRefused},
		"garbage":  {`<html>SECRET-TEXT</html>`, CodeRefused},
	} {
		t.Run(name, func(t *testing.T) {
			r := newDeviceRig(t, tc.body)
			s := r.start(t)
			r.advance(6 * time.Second)
			p := r.svc.PollDevice(context.Background(), s.State, "bind")
			if p.Status != "error" || p.Error != tc.code {
				t.Fatalf("%+v", p)
			}
			if strings.Contains(p.Error, "SECRET") || len(r.accounts.made) != 0 {
				t.Fatalf("provider text or an account leaked: %+v %d", p, len(r.accounts.made))
			}
		})
	}
}

func TestAnotherBrowserCannotPollOrReadAnOutcome(t *testing.T) {
	r := newDeviceRig(t, `{"access_token":"GH-ACCESS"}`)
	s := r.start(t)
	r.advance(6 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "other"); p.Status != "unknown" {
		t.Fatalf("%+v", p)
	}
	if p := r.svc.PollDevice(context.Background(), s.State, ""); p.Status != "unknown" {
		t.Fatalf("no binding: %+v", p)
	}
	if p := r.svc.PollDevice(context.Background(), "nope", "bind"); p.Status != "unknown" {
		t.Fatalf("%+v", p)
	}
	if r.p.tokens.Load() != 0 {
		t.Fatal("a foreign poll reached the provider")
	}
}

func TestTwoSimultaneousPollsMakeOneProviderCall(t *testing.T) {
	r := newDeviceRig(t, `{"error":"authorization_pending"}`)
	r.p.pollWait = make(chan struct{})
	s := r.start(t)
	r.advance(6 * time.Second)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.svc.PollDevice(context.Background(), s.State, "bind")
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(r.p.pollWait)
	wg.Wait()
	if n := r.p.tokens.Load(); n != 1 {
		t.Fatalf("%d provider calls", n)
	}
}

func TestADeviceLoginExpiresWithItsCode(t *testing.T) {
	r := newDeviceRig(t, `{"access_token":"GH-ACCESS"}`)
	s := r.start(t)
	r.advance(901 * time.Second)
	if p := r.svc.PollDevice(context.Background(), s.State, "bind"); p.Status != "unknown" || r.p.tokens.Load() != 0 {
		t.Fatalf("%+v, %d calls", p, r.p.tokens.Load())
	}
}

func TestOnlyADeviceProviderStartsADeviceLogin(t *testing.T) {
	r := newDeviceRig(t)
	r.svc.Registry = fakeRegistry(t, "https://unused.invalid/token")
	if _, err := r.svc.StartDevice(context.Background(), "claude", "bind"); err != ErrUnsupported {
		t.Fatalf("a redirect login started as a device login: %v", err)
	}
	r.svc.Registry = r.p.registry(t)
	r.svc.ClientID = func(string) string { return "" }
	if _, err := r.svc.StartDevice(context.Background(), "gh", "bind"); err != ErrDisabled {
		t.Fatalf("%v", err)
	}
}

func TestADeviceAnswerIsCheckedBeforeItIsShown(t *testing.T) {
	good := `{"device_code":"d","user_code":"U","verification_uri":"https://h.example/x","expires_in":60,"interval":3}`
	if g, err := parseGrant([]byte(good), ""); err != nil || g.Interval != 3*time.Second || g.ExpiresIn != time.Minute {
		t.Fatalf("%+v %v", g, err)
	}
	if g, _ := parseGrant([]byte(`{"device_code":"d","verification_uri":"https://h.example/x","verification_uri_complete":"https://h.example/x?c=1"}`), ""); g.VerificationURI != "https://h.example/x?c=1" || g.Interval != defaultInterval || g.ExpiresIn != defaultLife {
		t.Fatalf("complete address and defaults: %+v", g)
	}
	if g, _ := parseGrant([]byte(`{"device_code":"d","verification_uri":"https://h.example/x","interval":9999,"expires_in":99999}`), ""); g.Interval != time.Minute || g.ExpiresIn != maxLife {
		t.Fatalf("limits: %+v", g)
	}
	for name, raw := range map[string]string{
		"not an object":    `[]`,
		"no device code":   `{"verification_uri":"https://h.example/x"}`,
		"plain http page":  `{"device_code":"d","verification_uri":"http://h.example/x"}`,
		"script page":      `{"device_code":"d","verification_uri":"javascript:alert(1)"}`,
		"page with a user": `{"device_code":"d","verification_uri":"https://u:p@h.example/x"}`,
		"no page":          `{"device_code":"d"}`,
		"control in code":  `{"device_code":"d\u0000","verification_uri":"https://h.example/x"}`,
		"long user code":   `{"device_code":"d","user_code":"` + strings.Repeat("A", 65) + `","verification_uri":"https://h.example/x"}`,
	} {
		if _, err := parseGrant([]byte(raw), ""); ClassOf(err) != ClassMalformed {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func FuzzParseGrant(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `{"device_code":"d","verification_uri":"https://h/x"}`, `{"interval":-1}`, `null`, `{"device_code":1}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		g, err := parseGrant(raw, "")
		if err != nil {
			return
		}
		if g.DeviceCode == "" || !plainHTTPS(g.VerificationURI) || g.Interval < minInterval || g.Interval > maxInterval || g.ExpiresIn <= 0 || g.ExpiresIn > maxLife {
			t.Fatalf("accepted %q as %+v", raw, g)
		}
	})
}

func TestTheHookTableMatchesTheNamesADataFileMayUse(t *testing.T) {
	var have []string
	for n := range hooks {
		have = append(have, n)
	}
	want := upstream.HookNames()
	slices.Sort(have)
	slices.Sort(want)
	if !slices.Equal(have, want) {
		t.Fatalf("hooks %v, schema %v", have, want)
	}
}

func TestTheMintSendsOnlyItsOwnHeaders(t *testing.T) {
	r := newDeviceRig(t, `{"access_token":"GH-ACCESS"}`)
	var got http.Header
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		got = req.Header.Clone()
		_, _ = w.Write([]byte(`{"token":"T","expires_at":4102444800}`))
	}))
	defer srv.Close()
	e, _ := r.svc.Registry.ByID("gh")
	o := *e.OAuth
	o.Endpoints = map[string]string{upstream.EndpointCopilotToken: srv.URL}
	if _, _, err := mint(context.Background(), &TokenClient{HTTP: srv.Client()}, &o, "GH-ACCESS"); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "token GH-ACCESS" || got.Get("User-Agent") != "UA/1" || got.Get("Editor-Version") != "ed/1" {
		t.Fatalf("%v", got)
	}
	if got.Get("Copilot-Integration-Id") != "" {
		t.Fatal("a chat header went to the mint")
	}
}
