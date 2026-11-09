package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/cache"
	"rllmx/app/dialect"
	"rllmx/app/repository"
	"rllmx/app/upstream"
	"rllmx/tests"
	"rllmx/tests/store"
)

// hit is one call the fake provider received.
type hit struct {
	Method, Path, Body string
	Header             http.Header
}

// fake is a TLS provider whose answers a test scripts per path. A path with
// several answers walks them in order and repeats the last.
type fake struct {
	*httptest.Server
	mu    sync.Mutex
	hits  []hit
	reply map[string][]fakeReply
	calls map[string]*atomic.Int64
}

type fakeReply struct {
	Status int
	Body   string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{reply: map[string][]fakeReply{}, calls: map[string]*atomic.Int64{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		f.mu.Lock()
		f.hits = append(f.hits, hit{r.Method, r.URL.Path, string(raw[:n]), r.Header.Clone()})
		seq := f.reply[r.URL.Path]
		c := f.calls[r.URL.Path]
		if c == nil {
			c = new(atomic.Int64)
			f.calls[r.URL.Path] = c
		}
		i := int(c.Add(1)) - 1
		f.mu.Unlock()
		if len(seq) == 0 {
			w.WriteHeader(404)
			return
		}
		rep := seq[min(i, len(seq)-1)]
		if rep.Status != 0 {
			w.WriteHeader(rep.Status)
		}
		_, _ = w.Write([]byte(rep.Body))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fake) on(path string, replies ...fakeReply) {
	f.mu.Lock()
	f.reply[path] = replies
	f.mu.Unlock()
}

func (f *fake) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.calls[path]; c != nil {
		return int(c.Load())
	}
	return 0
}

func (f *fake) last(path string) hit {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.hits) - 1; i >= 0; i-- {
		if f.hits[i].Path == path {
			return f.hits[i]
		}
	}
	return hit{}
}

func ok(body string) fakeReply { return fakeReply{Body: body} }

// loginRig runs a device login of one provider file against the fake.
type loginRig struct {
	svc      *Service
	accounts *fakeAccounts
	f        *fake
	now      time.Time
	mu       sync.Mutex
	waits    []time.Duration
	reg      *upstream.Registry
}

func newLoginRig(t *testing.T, id, oauthBlock string) *loginRig {
	t.Helper()
	f := newFake(t)
	block := strings.NewReplacer("@", f.URL).Replace(oauthBlock)
	file := `{"id":"` + id + `","category":"oauth","transport":{"baseUrl":"https://x.example/v1/chat/completions","format":"openai"},"models":[],"oauth":` + block + `}`
	reg, err := upstream.Load(fstest.MapFS{"data/" + id + ".json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	r := &loginRig{f: f, accounts: &fakeAccounts{}, now: time.Unix(1_700_000_000, 0), reg: reg}
	clock := func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }
	sessions := NewSessions()
	sessions.Now = clock
	r.svc = &Service{
		Registry: reg, ClientID: func(string) string { return "client-" + id }, Sessions: sessions, Now: clock,
		Tokens: &TokenClient{HTTP: f.Client(), Sleep: func(_ context.Context, d time.Duration) error {
			r.mu.Lock()
			r.waits = append(r.waits, d)
			r.mu.Unlock()
			return nil
		}},
		Accounts: func() Accounts { return r.accounts },
	}
	return r
}

func (r *loginRig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

// run starts a login, waits out one interval and polls once.
func (r *loginRig) run(t *testing.T, id string) DevicePolled {
	t.Helper()
	s, err := r.svc.StartDevice(context.Background(), id, "bind")
	if err != nil {
		t.Fatal(err)
	}
	r.advance(time.Duration(s.Interval+1) * time.Second)
	return r.svc.PollDevice(context.Background(), s.State, "bind")
}

func (r *loginRig) made(t *testing.T) repository.ConnInput {
	t.Helper()
	if len(r.accounts.made) != 1 {
		t.Fatalf("%d accounts stored", len(r.accounts.made))
	}
	return r.accounts.made[0]
}

func psdOf(t *testing.T, in repository.ConnInput) map[string]any {
	t.Helper()
	m := map[string]any{}
	if in.PSD != "" {
		if err := json.Unmarshal([]byte(in.PSD), &m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

const deviceBlock = `{"flow":"device","deviceUrl":"@/device","tokenUrl":"@/token","scope":"s"`

func TestCallHeadersCarryTheAccountsDeviceIDOnEveryCall(t *testing.T) {
	r := newLoginRig(t, "kim", deviceBlock+`,"callHeaders":{"X-Dev":"{deviceId}","X-Fixed":"f"},"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.f.on("/device", ok(`{"device_code":"DC","user_code":"UC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	if p := r.run(t, "kim"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	start, poll := r.f.last("/device"), r.f.last("/token")
	id := start.Header.Get("X-Dev")
	if id == "" || poll.Header.Get("X-Dev") != id || start.Header.Get("X-Fixed") != "f" {
		t.Fatalf("start %v poll %v", start.Header, poll.Header)
	}
	if got := psdOf(t, r.made(t))["deviceId"]; got != id {
		t.Fatalf("stored device id %v, sent %q", got, id)
	}
	if strings.Contains(start.Body+poll.Body, id) {
		t.Fatal("the device id went in a body")
	}
}

func TestADeviceIDIsMadeOnlyWhenTheDataAsksForOne(t *testing.T) {
	r := newLoginRig(t, "plain", deviceBlock+`}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"AT"}`))
	if p := r.run(t, "plain"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if _, has := psdOf(t, r.made(t))["deviceId"]; has {
		t.Fatal("a device id was stored with no header asking for it")
	}
}

func TestTwoLoginsGetTwoDeviceIDs(t *testing.T) {
	r := newLoginRig(t, "kim", deviceBlock+`,"callHeaders":{"X-Dev":"{deviceId}"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"AT"}`))
	a, _ := r.svc.StartDevice(context.Background(), "kim", "bind")
	first := r.f.last("/device").Header.Get("X-Dev")
	b, _ := r.svc.StartDevice(context.Background(), "kim", "bind")
	second := r.f.last("/device").Header.Get("X-Dev")
	if a.State == b.State || first == "" || first == second {
		t.Fatalf("ids %q %q", first, second)
	}
}

func TestExtraStartParametersAreSentAndTheClientIDIsNotDuplicated(t *testing.T) {
	r := newLoginRig(t, "grk", deviceBlock+`,"params":[{"name":"referrer","value":"grok-build"}]}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"AT"}`))
	r.run(t, "grk")
	body := r.f.last("/device").Body
	for _, want := range []string{"referrer=grok-build", "client_id=client-grk", "scope=s"} {
		if !strings.Contains(body, want) {
			t.Errorf("start body %q lacks %q", body, want)
		}
	}
}

func TestAStartAnswerWithNoPageFallsBackToTheDataAddress(t *testing.T) {
	r := newLoginRig(t, "kim", deviceBlock+`,"endpoints":{"verify":"https://v.example/authorize"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","user_code":"AB 12"}`))
	s, err := r.svc.StartDevice(context.Background(), "kim", "bind")
	if err != nil || s.VerificationURI != "https://v.example/authorize" {
		t.Fatalf("a user code with a space: %+v %v", s, err)
	}
	r.f.on("/device", ok(`{"device_code":"DC","user_code":"AB-12"}`))
	s, err = r.svc.StartDevice(context.Background(), "kim", "bind")
	if err != nil || s.VerificationURI != "https://v.example/authorize?user_code=AB-12" {
		t.Fatalf("%+v %v", s, err)
	}
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://own.example/p"}`))
	s, _ = r.svc.StartDevice(context.Background(), "kim", "bind")
	if s.VerificationURI != "https://own.example/p" {
		t.Fatalf("the provider's own page lost: %+v", s)
	}
}

func TestPollingBySubmittedCodeNeedsNoClientIDForANoClientLogin(t *testing.T) {
	r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
	r.svc.ClientID = func(string) string { return "" }
	r.f.on("/device", ok(`{"code":"ABC123","verificationUrl":"https://app.example/auth?c=ABC123","expiresIn":120}`))
	r.f.on("/codes/ABC123", ok(`{"status":"approved","token":"KILO-TOKEN","userEmail":"k@x.test"}`))
	p := r.run(t, "kc")
	if p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.made(t)
	if in.Email != "k@x.test" || in.Fields["accessToken"] != `"KILO-TOKEN"` {
		t.Fatalf("%+v", in)
	}
	if _, has := in.Fields["refreshToken"]; has {
		t.Fatal("a refresh token was invented")
	}
	start := r.f.last("/device")
	if start.Method != "POST" || start.Body != "" || start.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("start: %+v", start)
	}
	if r.f.last("/codes/ABC123").Method != "GET" {
		t.Fatal("the poll is not a GET")
	}
}

func TestAClientIDIsStillRequiredWhenTheDataDoesNotWaiveIt(t *testing.T) {
	r := newLoginRig(t, "plain", deviceBlock+`}`)
	r.svc.ClientID = func(string) string { return "" }
	if _, err := r.svc.StartDevice(context.Background(), "plain", "bind"); err != ErrDisabled {
		t.Fatalf("%v", err)
	}
}

func TestTheKiloPollIsReadByItsHTTPStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		reply  fakeReply
		status string
		code   string
	}{
		"waiting":                     {fakeReply{Status: 202, Body: `{}`}, "pending", ""},
		"approved without a token":    {ok(`{"status":"approved"}`), "pending", ""},
		"a token before approval":     {ok(`{"status":"pending","token":"T"}`), "pending", ""},
		"refused":                     {fakeReply{Status: 403, Body: `{"error":"SECRET"}`}, "error", CodeDenied},
		"expired":                     {fakeReply{Status: 410, Body: `{}`}, "error", CodeExpired},
		"a server error":              {fakeReply{Status: 500, Body: `<html>SECRET</html>`}, "error", "transport"},
		"another client error":        {fakeReply{Status: 404, Body: `x`}, "error", CodeRefused},
		"not json":                    {ok(`<html>`), "error", CodeRefused},
		"a token with a control byte": {ok("{\"status\":\"approved\",\"token\":\"a\\u0000b\"}"), "error", CodeRefused},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
			r.f.on("/device", ok(`{"code":"ABC123","verificationUrl":"https://app.example/a"}`))
			r.f.on("/codes/ABC123", tc.reply)
			p := r.run(t, "kc")
			if p.Status != tc.status || p.Error != tc.code {
				t.Fatalf("%+v", p)
			}
			if strings.Contains(p.Error, "SECRET") {
				t.Fatal("provider text in the answer")
			}
		})
	}
}

func TestTheKiloStartIsCheckedBeforeItIsShown(t *testing.T) {
	for name, tc := range map[string]struct {
		reply fakeReply
		class Class
	}{
		"too many pending":       {fakeReply{Status: 429, Body: `x`}, ClassRejected},
		"a server error":         {fakeReply{Status: 502, Body: `x`}, ClassTransport},
		"not an object":          {ok(`[]`), ClassMalformed},
		"a code with a slash":    {ok(`{"code":"a/b","verificationUrl":"https://app.example/a"}`), ClassMalformed},
		"a code with dots":       {ok(`{"code":"..","verificationUrl":"https://app.example/a"}`), ClassMalformed},
		"a code with a query":    {ok(`{"code":"a?b=1","verificationUrl":"https://app.example/a"}`), ClassMalformed},
		"no code":                {ok(`{"verificationUrl":"https://app.example/a"}`), ClassMalformed},
		"a page over plain http": {ok(`{"code":"abc","verificationUrl":"http://app.example/a"}`), ClassMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
			r.f.on("/device", tc.reply)
			_, err := r.svc.StartDevice(context.Background(), "kc", "bind")
			if ClassOf(err) != tc.class {
				t.Fatalf("%v", err)
			}
		})
	}
}

// The poll puts the code in a request path, so it checks the code again even
// though the start already did.
func TestTheKiloPollRefusesACodeThatCouldChangeTheAddress(t *testing.T) {
	r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x"}`)
	r.f.on("/other", ok(`{"status":"approved","token":"STOLEN"}`))
	e, _ := r.reg.ByID("kc")
	flow, err := NewFlow(e, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"../other", "a/b", "a?x=1", "a#f", "", "a b", strings.Repeat("a", 65)} {
		_, _, err := kiloPoll(context.Background(), r.svc.Tokens, flow, code, nil)
		if ClassOf(err) != ClassMalformed {
			t.Errorf("%q: %v", code, err)
		}
	}
	if n := r.f.count("/other") + r.f.count("/codes/a"); n != 0 {
		t.Fatalf("%d requests left for a refused code", n)
	}
}

// FuzzAgentKeyAnswer feeds the Nous mint any body: it must never panic, and a key
// it accepts is bounded and free of control characters.
func FuzzAgentKeyAnswer(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `x`, `{"api_key":"k"}`, `{"api_key":"k","expires_at":"2030-01-01T00:00:00Z","key_id":"a"}`, `{"api_key":1,"expires_at":-1}`, "{\"api_key\":\"a\\u0000\"}"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		defer srv.Close()
		fl := Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointAgentKey: srv.URL}}}
		key, until, id := mintAgentKey(context.Background(), &TokenClient{HTTP: srv.Client()}, fl, "AT", nil)
		if key != "" && (len(key) > maxCredential || strings.ContainsAny(key, "\x00\r\n")) {
			t.Fatalf("accepted key %q", key)
		}
		if key == "" && (!until.IsZero() || id != "") {
			t.Fatalf("details without a key: %v %q", until, id)
		}
	})
}

// FuzzMuseKeyAnswer does the same for the Meta key mint: an accepted login holds
// a bounded key and nothing the provider wrote as an address with a space.
func FuzzMuseKeyAnswer(f *testing.F) {
	for _, s := range []string{`{}`, `[]`, `{"api_key":"k"}`, `{"api_key":"k","is_subs_active":false}`, `{"api_key":"k","user_email":" A@B.C "}`, `{"api_key":"k","require_payment":true}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
		defer srv.Close()
		fl := Flow{OAuth: upstream.OAuth{Endpoints: map[string]string{upstream.EndpointKey: srv.URL}}}
		acc, err := museAccount(context.Background(), &TokenClient{HTTP: srv.Client()}, fl, Tokens{Access: "META"}, nil)
		if err != nil {
			if acc.Access != "" {
				t.Fatal("a failed hook returned a credential")
			}
			return
		}
		if acc.Access == "" || len(acc.Access) > maxCredential || strings.ContainsAny(acc.Access, "\x00\r\n") || !acc.NoRefresh {
			t.Fatalf("accepted %+v", acc)
		}
	})
}

func FuzzKiloCode(f *testing.F) {
	for _, s := range []string{"abc", "a/b", "..", "a?b", "", "a b", "é", strings.Repeat("a", 65), "a-b_c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, code string) {
		if !kiloCode.MatchString(code) {
			return
		}
		if strings.ContainsAny(code, "/?#%\\ .:") || len(code) > 64 || code == "" {
			t.Fatalf("accepted %q", code)
		}
	})
}

// A hook that fails ends the login and stores nothing.
func TestAMuseLoginTradesTheMetaTokenForTheKeyAndKeepsOnlyTheKey(t *testing.T) {
	r := newLoginRig(t, "mus", deviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"},"callHeaders":{"x-api-version":"1.0.0"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"META-TOKEN","refresh_token":"META-RT","expires_in":99}`))
	r.f.on("/key", ok(`{"api_key":"LLM|KEY","user_email":"  Person@X.Test ","subs_tier_name":"pro","is_subs_active":true}`))
	if p := r.run(t, "mus"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.made(t)
	if in.Fields["accessToken"] != `"LLM|KEY"` || in.Email != "person@x.test" {
		t.Fatalf("%+v", in)
	}
	for _, k := range []string{"refreshToken", "expiresAt", "expiresIn"} {
		if _, has := in.Fields[k]; has {
			t.Errorf("%s was stored for a key that cannot be renewed", k)
		}
	}
	all := in.PSD
	for _, f := range in.Fields {
		all += f
	}
	if strings.Contains(all, "META-TOKEN") || strings.Contains(all, "META-RT") {
		t.Fatalf("the Meta token was stored: %s", all)
	}
	if psdOf(t, in)["subscriptionTier"] != "pro" {
		t.Fatalf("%s", in.PSD)
	}
	hit := r.f.last("/key")
	if hit.Header.Get("Authorization") != "Bearer META-TOKEN" || hit.Header.Get("X-Api-Version") != "1.0.0" || hit.Body != `{"onboard":true}` {
		t.Fatalf("%+v", hit)
	}
}

func TestAMuseKeyThatCannotBeHadEndsTheLoginWithAFixedWord(t *testing.T) {
	for name, key := range map[string][]fakeReply{
		"no key":            {ok(`{"user_email":"a@b.c"}`)},
		"an inactive plan":  {ok(`{"api_key":"K","is_subs_active":false}`)},
		"a payment demand":  {ok(`{"require_payment":true,"action_url":"https://pay.example"}`)},
		"a client error":    {{Status: 403, Body: `{"title":"SECRET"}`}},
		"not json":          {ok(`<html>SECRET`)},
		"a key with a byte": {ok("{\"api_key\":\"a\\u0000b\"}")},
		"a huge key":        {ok(`{"api_key":"` + strings.Repeat("k", maxCredential+1) + `"}`)},
		"always busy":       {{Status: 429, Body: `x`}},
		"always failing":    {{Status: 503, Body: `x`}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "mus", deviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
			r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
			r.f.on("/token", ok(`{"access_token":"META-TOKEN"}`))
			r.f.on("/key", key...)
			p := r.run(t, "mus")
			if p.Status != "error" || strings.Contains(p.Error, "SECRET") || len(r.accounts.made) != 0 {
				t.Fatalf("%+v, %d accounts", p, len(r.accounts.made))
			}
		})
	}
}

func TestAMuseKeyIsTriedAgainOnABusyAnswerWithGrowingPauses(t *testing.T) {
	r := newLoginRig(t, "mus", deviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"META-TOKEN"}`))
	r.f.on("/key", fakeReply{Status: 429, Body: `x`}, fakeReply{Status: 503, Body: `x`}, ok(`{"api_key":"K"}`))
	if p := r.run(t, "mus"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	if r.f.count("/key") != 3 || len(r.waits) != 2 || r.waits[0] != 5*time.Second || r.waits[1] != 10*time.Second {
		t.Fatalf("%d calls, waits %v", r.f.count("/key"), r.waits)
	}
}

func TestAMuseRetryEndsWhenTheRequestDoes(t *testing.T) {
	r := newLoginRig(t, "mus", deviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
	r.svc.Tokens.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"META-TOKEN"}`))
	r.f.on("/key", fakeReply{Status: 429, Body: `x`})
	p := r.run(t, "mus")
	if p.Status != "error" || r.f.count("/key") != 1 || len(r.accounts.made) != 0 {
		t.Fatalf("%+v, %d calls", p, r.f.count("/key"))
	}
}

func TestANousLoginStoresTheAgentKeyAsTheKeyAndCapsTheExpiry(t *testing.T) {
	r := newLoginRig(t, "nou", deviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"},"refresh":{"encoding":"form","leadSeconds":300,"tokenHeader":"x-rt","hook":"nous"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"NOUS-AT","refresh_token":"NOUS-RT","expires_in":86400}`))
	soon := r.now.Add(2 * time.Hour).Unix()
	r.f.on("/agent", ok(`{"api_key":"AGENT-KEY","key_id":"k1","expires_at":`+itoa(soon)+`}`))
	if p := r.run(t, "nou"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.made(t)
	if in.Fields["apiKey"] != `"AGENT-KEY"` || in.Fields["accessToken"] != `"NOUS-AT"` || in.Fields["refreshToken"] != `"NOUS-RT"` {
		t.Fatalf("%+v", in.Fields)
	}
	if want := `"` + dialect.FormatTimestamp(time.Unix(soon, 0)) + `"`; in.Fields["expiresAt"] != want {
		t.Fatalf("expiresAt %s, want %s", in.Fields["expiresAt"], want)
	}
	if got := psdOf(t, in)["agentKeyId"]; got != "k1" {
		t.Fatalf("%s", in.PSD)
	}
	if h := r.f.last("/agent"); h.Header.Get("Authorization") != "Bearer NOUS-AT" || h.Body != `{"min_ttl_seconds":7200}` {
		t.Fatalf("%+v", h)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestANousLoginWithoutAnAgentKeyUsesTheAccessTokenAndItsOwnExpiry(t *testing.T) {
	for name, rep := range map[string]fakeReply{
		"a server error": {Status: 500, Body: `x`}, "no key": ok(`{}`), "a bad key": ok("{\"api_key\":\"a\\u0000\"}"), "not json": ok(`x`),
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "nou", deviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"}}`)
			r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
			r.f.on("/token", ok(`{"access_token":"NOUS-AT","refresh_token":"RT","expires_in":3600}`))
			r.f.on("/agent", rep)
			if p := r.run(t, "nou"); p.Status != "done" {
				t.Fatalf("%+v", p)
			}
			in := r.made(t)
			if _, has := in.Fields["apiKey"]; has || in.Fields["accessToken"] != `"NOUS-AT"` {
				t.Fatalf("%+v", in.Fields)
			}
			if in.Fields["expiresIn"] != "3600" {
				t.Fatalf("%+v", in.Fields)
			}
		})
	}
}

func TestAnAgentKeyThatLivesLongerThanTheTokenDoesNotExtendIt(t *testing.T) {
	r := newLoginRig(t, "nou", deviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	r.f.on("/agent", ok(`{"api_key":"K","expires_at":`+itoa(r.now.Add(48*time.Hour).Unix())+`}`))
	r.run(t, "nou")
	want := `"` + dialect.FormatTimestamp(r.now.Add(time.Duration(3600)*time.Second).Add(-0)) + `"`
	got := r.made(t).Fields["expiresAt"]
	// The poll ran one interval + 1 s after the start, and expires_in counts from there.
	if got == `"`+dialect.FormatTimestamp(r.now.Add(48*time.Hour))+`"` {
		t.Fatalf("the longer expiry won: %s (%s)", got, want)
	}
}

func TestAKiloLoginReadsTheOrganisationAndIgnoresAnythingElse(t *testing.T) {
	for name, tc := range map[string]struct {
		profile fakeReply
		org     string
	}{
		"an organisation":        {ok(`{"organizations":[{"id":"org_1"},{"id":"org_2"}]}`), "org_1"},
		"none":                   {ok(`{"organizations":[]}`), ""},
		"a bad id":               {ok(`{"organizations":[{"id":"a/b"}]}`), ""},
		"an id that is a number": {ok(`{"organizations":[{"id":7}]}`), ""},
		"a server error":         {fakeReply{Status: 500, Body: `x`}, ""},
		"not json":               {ok(`<html>`), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "kc", `{"flow":"device","dialect":"kilocode","noClient":true,"deviceUrl":"@/device","tokenUrl":"@/codes","scope":"x","hook":"kilocode","endpoints":{"profile":"@/profile"}}`)
			r.f.on("/device", ok(`{"code":"ABC123","verificationUrl":"https://app.example/a"}`))
			r.f.on("/codes/ABC123", ok(`{"status":"approved","token":"KILO"}`))
			r.f.on("/profile", tc.profile)
			if p := r.run(t, "kc"); p.Status != "done" {
				t.Fatalf("%+v", p)
			}
			in := r.made(t)
			if got, _ := psdOf(t, in)["orgId"].(string); got != tc.org {
				t.Fatalf("orgId %q, want %q", got, tc.org)
			}
			if h := r.f.last("/profile"); h.Header.Get("Authorization") != "Bearer KILO" {
				t.Fatalf("%v", h.Header)
			}
		})
	}
}

func jwtFor(payload string) string { return jwt(payload) }

func TestAGrokLoginNamesTheAccountFromTheTokensAndNeverFromTheSubject(t *testing.T) {
	r := newLoginRig(t, "grk", deviceBlock+`,"params":[{"name":"referrer","value":"grok-build"}],"hook":"grokCli","endpoints":{"user":"@/user"},"callHeaders":{"x-grok-client-version":"1.0.44"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	id := jwtFor(`{"sub":"subject-id-1234"}`)
	r.f.on("/token", ok(`{"access_token":"`+jwtFor(`{"sub":"subject-id-1234"}`)+`","refresh_token":"RT","id_token":"`+id+`","expires_in":600}`))
	r.f.on("/user", ok(`{"email":"profile@x.test","userId":"U-1","subscriptionTier":"super","hasGrokCodeAccess":true}`))
	if p := r.run(t, "grk"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
	in := r.made(t)
	psd := psdOf(t, in)
	if in.Email != "profile@x.test" || psd["email"] != "profile@x.test" || psd["userId"] != "U-1" || psd["subscriptionTier"] != "super" || psd["hasGrokCodeAccess"] != true {
		t.Fatalf("%+v %s", in, in.PSD)
	}
	if in.Fields["idToken"] == "" {
		t.Fatal("the id token is not stored at the top level")
	}
	if h := r.f.last("/user"); h.Header.Get("X-Grok-Client-Version") != "1.0.44" || h.Header.Get("Authorization") == "" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAGrokEmailComesFromTheIDTokenFirstThenTheAccessTokenThenTheProfile(t *testing.T) {
	cases := map[string]struct {
		id, access, profile, want string
	}{
		"the id token":            {`{"email":"id@x.test"}`, `{"email":"at@x.test"}`, `{"email":"p@x.test"}`, "id@x.test"},
		"its user name":           {`{"preferred_username":"name@x.test"}`, ``, `{}`, "name@x.test"},
		"the access token":        {`{"sub":"s"}`, `{"email":"at@x.test"}`, `{"email":"p@x.test"}`, "at@x.test"},
		"the profile":             {`{"sub":"s"}`, `{"sub":"s"}`, `{"email":"p@x.test"}`, "p@x.test"},
		"nothing but a subject":   {`{"sub":"only-a-subject"}`, `{"sub":"only-a-subject"}`, `{}`, ""},
		"an address with a space": {`{"email":"a b@x.test"}`, ``, `{}`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLoginRig(t, "grk", deviceBlock+`,"hook":"grokCli","endpoints":{"user":"@/user"}}`)
			r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
			access := "plain-access"
			if tc.access != "" {
				access = jwtFor(tc.access)
			}
			r.f.on("/token", ok(`{"access_token":"`+access+`","id_token":"`+jwtFor(tc.id)+`"}`))
			r.f.on("/user", ok(tc.profile))
			if p := r.run(t, "grk"); p.Status != "done" {
				t.Fatalf("%+v", p)
			}
			if got := r.made(t).Email; got != tc.want {
				t.Fatalf("email %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAGrokLoginStillConnectsWhenTheProfileFails(t *testing.T) {
	r := newLoginRig(t, "grk", deviceBlock+`,"hook":"grokCli","endpoints":{"user":"@/user"}}`)
	r.f.on("/device", ok(`{"device_code":"DC","verification_uri":"https://v.example/x"}`))
	r.f.on("/token", ok(`{"access_token":"AT","refresh_token":"RT"}`))
	r.f.on("/user", fakeReply{Status: 500, Body: `x`})
	if p := r.run(t, "grk"); p.Status != "done" {
		t.Fatalf("%+v", p)
	}
}

// refreshRig renews an account of a device provider against the fake.
func newRefreshRig(t *testing.T, id, block string) (*loginRig, *Refresher, *store.Store) {
	t.Helper()
	r := newLoginRig(t, id, block)
	st := store.Migrated(t)
	rf := &Refresher{
		Flow: func(p string) (Flow, bool) {
			e, ok := r.reg.ByID(p)
			if !ok {
				return Flow{}, false
			}
			f, err := NewFlow(e, "client-"+p)
			return f, err == nil
		},
		Tokens: r.svc.Tokens, Cache: cache.New(tests.NewCache()), Store: func() Store { return st.Connections() },
		Now: func() time.Time { return r.now }, Log: &tests.Log{},
	}
	return r, rf, st
}

func seed(t *testing.T, st *store.Store, provider string, fields map[string]string, psd string) repository.Connection {
	t.Helper()
	c, err := st.Connections().Create(context.Background(), repository.ConnInput{Provider: provider, AuthType: "oauth", Email: "a@x.test", Fields: fields, PSD: psd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestANousRenewalSendsTheTokenInItsHeaderAndStoresTheNewKey(t *testing.T) {
	r, rf, st := newRefreshRig(t, "nou", deviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"},"refresh":{"encoding":"form","leadSeconds":300,"tokenHeader":"x-nous-refresh-token","hook":"nous"}}`)
	soon := r.now.Add(90 * time.Minute)
	r.f.on("/token", ok(`{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":86400}`))
	r.f.on("/agent", ok(`{"api_key":"NEW-KEY","expires_at":`+itoa(soon.Unix())+`}`))
	c := seed(t, st, "nou", map[string]string{
		"accessToken": `"OLD-AT"`, "refreshToken": `"OLD-RT"`, "apiKey": `"OLD-KEY"`, "testStatus": `"active"`,
		"expiresAt": `"` + dialect.FormatTimestamp(r.now.Add(time.Minute)) + `"`,
	}, "")
	got, err := rf.Refresh(context.Background(), c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	d := got.Doc()
	if v, _ := d.GetString("apiKey"); v != "NEW-KEY" {
		t.Fatalf("apiKey %q", v)
	}
	if v, _ := d.GetString("accessToken"); v != "NEW-AT" {
		t.Fatalf("accessToken %q", v)
	}
	if v, _ := d.GetString("expiresAt"); v != dialect.FormatTimestamp(soon) {
		t.Fatalf("expiresAt %q, want %q", v, dialect.FormatTimestamp(soon))
	}
	if h := r.f.last("/token"); h.Header.Get("X-Nous-Refresh-Token") != "OLD-RT" || !strings.Contains(h.Body, "refresh_token=OLD-RT") {
		t.Fatalf("%+v", h)
	}
	if h := r.f.last("/agent"); h.Header.Get("Authorization") != "Bearer NEW-AT" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAFailedAgentKeyKeepsTheKeyTheAccountHasAndStillStoresTheTokens(t *testing.T) {
	r, rf, st := newRefreshRig(t, "nou", deviceBlock+`,"hook":"nous","endpoints":{"agentKey":"@/agent"},"refresh":{"encoding":"form","leadSeconds":300,"hook":"nous"}}`)
	r.f.on("/token", ok(`{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":3600}`))
	r.f.on("/agent", fakeReply{Status: 500, Body: `x`})
	c := seed(t, st, "nou", map[string]string{"accessToken": `"OLD-AT"`, "refreshToken": `"OLD-RT"`, "apiKey": `"OLD-KEY"`, "testStatus": `"active"`}, "")
	got, err := rf.Refresh(context.Background(), c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	d := got.Doc()
	if v, _ := d.GetString("apiKey"); v != "OLD-KEY" {
		t.Fatalf("the old key was lost: %q", v)
	}
	if v, _ := d.GetString("refreshToken"); v != "NEW-RT" {
		t.Fatalf("refresh token %q", v)
	}
}

func TestAKimiRenewalCarriesItsHeadersWithTheStoredDeviceID(t *testing.T) {
	r, rf, st := newRefreshRig(t, "kim", deviceBlock+`,"callHeaders":{"X-Msh-Device-Id":"{deviceId}","X-Msh-Platform":"p"},"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.f.on("/token", ok(`{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":3600}`))
	c := seed(t, st, "kim", map[string]string{
		"accessToken": `"OLD"`, "refreshToken": `"OLD-RT"`, "testStatus": `"active"`,
		"expiresAt": `"` + dialect.FormatTimestamp(r.now.Add(time.Minute)) + `"`,
	}, `{"deviceId":"dev-42"}`)
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != nil {
		t.Fatal(err)
	}
	h := r.f.last("/token")
	if h.Header.Get("X-Msh-Device-Id") != "dev-42" || h.Header.Get("X-Msh-Platform") != "p" {
		t.Fatalf("%v", h.Header)
	}
}

func TestAnAccountWithNoDeviceIDSendsNoDeviceIDHeader(t *testing.T) {
	r, rf, st := newRefreshRig(t, "kim", deviceBlock+`,"callHeaders":{"X-Msh-Device-Id":"{deviceId}","X-Msh-Platform":"p"},"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.f.on("/token", ok(`{"access_token":"NEW-AT"}`))
	c := seed(t, st, "kim", map[string]string{"accessToken": `"OLD"`, "refreshToken": `"OLD-RT"`, "testStatus": `"active"`}, "")
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != nil {
		t.Fatal(err)
	}
	h := r.f.last("/token")
	if _, has := h.Header["X-Msh-Device-Id"]; has || h.Header.Get("X-Msh-Platform") != "p" {
		t.Fatalf("%v", h.Header)
	}
}

func TestALoginThatCannotBeRenewedIsNeverDueAndNeverMarkedDead(t *testing.T) {
	r, rf, st := newRefreshRig(t, "mus", deviceBlock+`,"hook":"muse","endpoints":{"key":"@/key"}}`)
	c := seed(t, st, "mus", map[string]string{"accessToken": `"KEY"`, "testStatus": `"active"`}, "")
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != ErrNoRefresh {
		t.Fatalf("%v", err)
	}
	if refreshed, err := rf.Ensure(context.Background(), c.ID); err != nil || refreshed {
		t.Fatalf("%v %v", refreshed, err)
	}
	if n := rf.Pass(context.Background(), 0); n != 0 {
		t.Fatalf("a pass renewed %d", n)
	}
	got, _, _ := st.Connections().Get(context.Background(), c.ID)
	if s, _ := got.Doc().GetString("testStatus"); s != "active" {
		t.Fatalf("%q", s)
	}
	if r.f.count("/token") != 0 {
		t.Fatal("the provider was called")
	}
}

func TestAnInvalidGrantStillMarksADeviceAccountDead(t *testing.T) {
	r, rf, st := newRefreshRig(t, "kim", deviceBlock+`,"refresh":{"encoding":"form","leadSeconds":300}}`)
	r.f.on("/token", fakeReply{Status: 400, Body: `{"error":"invalid_grant"}`})
	c := seed(t, st, "kim", map[string]string{"accessToken": `"OLD"`, "refreshToken": `"OLD-RT"`, "testStatus": `"active"`}, "")
	if _, err := rf.Refresh(context.Background(), c.ID, ""); err != ErrExpired {
		t.Fatalf("%v", err)
	}
}

func TestEveryRefreshAndDialectNameIsInItsTable(t *testing.T) {
	for _, n := range upstream.RefreshHookNames() {
		if refreshHooks[n] == nil {
			t.Errorf("refresh hook %q has no step", n)
		}
	}
	for n := range refreshHooks {
		found := false
		for _, s := range upstream.RefreshHookNames() {
			found = found || s == n
		}
		if !found {
			t.Errorf("step %q is not in the schema", n)
		}
	}
	for _, n := range upstream.DialectNames() {
		if _, ok := deviceDialects[n]; !ok {
			t.Errorf("dialect %q has no code", n)
		}
	}
	if len(deviceDialects) != len(upstream.DialectNames()) {
		t.Errorf("%d dialects in code, %d in the schema", len(deviceDialects), len(upstream.DialectNames()))
	}
}
