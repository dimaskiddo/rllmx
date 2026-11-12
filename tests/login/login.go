// Package login holds the fakes and rigs the login tests drive: a scripted TLS
// provider, the account store, and the two wire-shaped rigs. It imports the
// engine, so a provider package's test can use it too.
package login

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// Epoch is the clock every rig starts at.
var Epoch = time.Unix(1_700_000_000, 0)

// Hit is one call the fake provider received.
type Hit struct {
	Method, Path, Body, Query string
	Header                    http.Header
}

// Reply is one scripted answer.
type Reply struct {
	Status int
	Body   string
}

// OK is a 200 with the given body.
func OK(body string) Reply { return Reply{Body: body} }

// Fake is a TLS provider whose answers a test scripts per path. A path with
// several answers walks them in order and repeats the last.
type Fake struct {
	*httptest.Server
	mu    sync.Mutex
	hits  []Hit
	reply map[string][]Reply
	calls map[string]*atomic.Int64
}

// NewFake starts a TLS provider and stops it with the test.
func NewFake(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{reply: map[string][]Reply{}, calls: map[string]*atomic.Int64{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		f.mu.Lock()
		f.hits = append(f.hits, Hit{Method: r.Method, Path: r.URL.Path, Body: string(raw[:n]), Query: r.URL.RawQuery, Header: r.Header.Clone()})
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

// On scripts the answers of one path.
func (f *Fake) On(path string, replies ...Reply) {
	f.mu.Lock()
	f.reply[path] = replies
	f.mu.Unlock()
}

// Count is how many calls a path received.
func (f *Fake) Count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c := f.calls[path]; c != nil {
		return int(c.Load())
	}
	return 0
}

// Last is the last call a path received.
func (f *Fake) Last(path string) Hit {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.hits) - 1; i >= 0; i-- {
		if f.hits[i].Path == path {
			return f.hits[i]
		}
	}
	return Hit{}
}

// Accounts is the account store a login writes to.
type Accounts struct {
	mu   sync.Mutex
	made []repository.ConnInput
}

// Create records the account the engine stores.
func (f *Accounts) Create(_ context.Context, in repository.ConnInput, _ *bool) (repository.Connection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.made = append(f.made, in)
	return repository.Connection{ID: "conn-" + strconv.Itoa(len(f.made))}, nil
}

// All is every account stored, in order.
func (f *Accounts) All() []repository.ConnInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]repository.ConnInput{}, f.made...)
}

// Len is how many accounts were stored.
func (f *Accounts) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.made)
}

// Made is the one account a login stored.
func (f *Accounts) Made(t *testing.T) repository.ConnInput {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.made) != 1 {
		t.Fatalf("%d accounts stored", len(f.made))
	}
	return f.made[0]
}

// PSD is the stored account data, decoded.
func PSD(t *testing.T, in repository.ConnInput) map[string]any {
	t.Helper()
	m := map[string]any{}
	if in.PSD != "" {
		if err := json.Unmarshal([]byte(in.PSD), &m); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

// Reroute sends every call to the given server.
func Reroute(srv *httptest.Server) *http.Client {
	base := srv.Client()
	target, _ := url.Parse(srv.URL)
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return base.Transport.RoundTrip(r)
	})}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Registry is the program's own provider data.
func Registry(t *testing.T) *upstream.Registry {
	t.Helper()
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// Login runs one provider file against a fake. The table is the steps its
// dialects name; nil follows the standard calls only.
type Login struct {
	Service  *oauth.Service
	Accounts *Accounts
	F        *Fake
	Now      time.Time
	mu       sync.Mutex
	Waits    []time.Duration
	Reg      *upstream.Registry
}

// NewLogin builds the rig for one provider file. "@" in the block is the fake's
// address.
func NewLogin(t *testing.T, id, oauthBlock string, table oauth.Dialects) *Login {
	t.Helper()
	f := NewFake(t)
	block := strings.NewReplacer("@", f.URL).Replace(oauthBlock)
	file := `{"id":"` + id + `","category":"oauth","transport":{"baseUrl":"https://x.example/v1/chat/completions","format":"openai"},"models":[],"oauth":` + block + `}`
	reg, err := upstream.Load(fstest.MapFS{"data/" + id + ".json": {Data: []byte(file)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	r := &Login{F: f, Accounts: &Accounts{}, Now: Epoch, Reg: reg}
	clock := func() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.Now }
	sessions := oauth.NewSessions()
	sessions.Now = clock
	tokens := &oauth.TokenClient{HTTP: f.Client(), Now: clock, Dialects: table, Sleep: func(_ context.Context, d time.Duration) error {
		r.mu.Lock()
		r.Waits = append(r.Waits, d)
		r.mu.Unlock()
		return nil
	}}
	r.Service = &oauth.Service{
		Registry: reg, ClientID: func(string) string { return "client-" + id }, Sessions: sessions, Now: clock,
		Dialects: table,
		Tokens:   tokens,
		Accounts: func() oauth.Accounts { return r.Accounts },
	}
	return r
}

// Advance moves the rig's clock.
func (r *Login) Advance(d time.Duration) { r.mu.Lock(); r.Now = r.Now.Add(d); r.mu.Unlock() }

// Run starts a login, waits out one interval and polls once.
func (r *Login) Run(t *testing.T, id string) oauth.DevicePolled {
	t.Helper()
	s, err := r.Service.StartDevice(context.Background(), id, "bind")
	if err != nil {
		t.Fatal(err)
	}
	r.Advance(time.Duration(s.Interval+1) * time.Second)
	return r.Service.PollDevice(context.Background(), s.State, "bind")
}

// DeviceBlock is the smallest device login a test can start.
const DeviceBlock = `{"flow":"device","deviceUrl":"@/device","tokenUrl":"@/token","scope":"s"`

// Good answers a renewal with a fresh set.
func Good(int, string) (int, string) {
	return 200, `{"access_token":"NEW-AT","refresh_token":"NEW-RT","expires_in":3600,"id_token":"NEW-ID"}`
}

// Seed stores an account straight, for a renewal to find.
func Seed(t *testing.T, st *store.Store, provider string, fields map[string]string, psd string) repository.Connection {
	t.Helper()
	c, err := st.Connections().Create(context.Background(), repository.ConnInput{Provider: provider, AuthType: "oauth", Email: "a@x.test", Fields: fields, PSD: psd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// NewRefresher builds the renewal on a login rig's own registry and clock.
func NewRefresher(t *testing.T, l *Login, table oauth.Dialects) (*oauth.Refresher, *store.Store) {
	t.Helper()
	st := store.Migrated(t)
	rf := &oauth.Refresher{
		Dialects: table,
		Flow: func(p string) (oauth.Flow, bool) {
			e, ok := l.Reg.ByID(p)
			if !ok {
				return oauth.Flow{}, false
			}
			f, err := oauth.NewFlow(e, "client-"+p)
			return f, err == nil
		},
		Tokens: l.Service.Tokens, Cache: cache.New(tests.NewCache()), Store: func() oauth.Store { return st.Connections() },
		Now: func() time.Time { return l.Now }, Log: &tests.Log{},
	}
	return rf, st
}

// TokenEndpoint is a TLS token endpoint that records each body and answers from
// a function.
type TokenEndpoint struct {
	*httptest.Server
	mu     sync.Mutex
	bodies []string
	types  []string
	calls  atomic.Int64
}

// NewTokenEndpoint starts a token endpoint and stops it with the test.
func NewTokenEndpoint(t *testing.T, answer func(call int, body string) (int, string)) *TokenEndpoint {
	t.Helper()
	e := &TokenEndpoint{}
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

// Renew runs renewals against the token endpoint.
type Renew struct {
	R     *oauth.Refresher
	St    *store.Store
	End   *TokenEndpoint
	Log   *tests.Log
	Waits []time.Duration
	Now   time.Time
	mu    sync.Mutex
}

// NewRenew builds the renewal rig. The table is the steps its dialects name.
func NewRenew(t *testing.T, answer func(call int, body string) (int, string), table oauth.Dialects) *Renew {
	t.Helper()
	rig := &Renew{St: store.Migrated(t), Log: &tests.Log{}, Now: Epoch}
	rig.End = NewTokenEndpoint(t, answer)
	reg := FakeRegistry(t, rig.End.URL)
	rig.R = &oauth.Refresher{
		Dialects: table,
		Flow: func(provider string) (oauth.Flow, bool) {
			id, ok := reg.Resolve(provider)
			if !ok {
				return oauth.Flow{}, false
			}
			e, _ := reg.ByID(id)
			f, err := oauth.NewFlow(e, "client-"+id)
			return f, err == nil
		},
		Tokens: &oauth.TokenClient{HTTP: rig.End.Client()},
		Cache:  cache.New(tests.NewCache()),
		Store:  func() oauth.Store { return rig.St.Connections() },
		Log:    rig.Log,
		Now:    func() time.Time { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.Now },
		Wait: func(_ context.Context, d time.Duration) error {
			rig.mu.Lock()
			rig.Waits = append(rig.Waits, d)
			rig.mu.Unlock()
			return nil
		},
	}
	return rig
}

// Conn stores an account for the renewal to find.
func (g *Renew) Conn(t *testing.T, provider, email string, kv map[string]string) repository.Connection {
	t.Helper()
	fields := map[string]string{}
	for k, v := range kv {
		b, _ := json.Marshal(v)
		fields[k] = string(b)
	}
	c, err := g.St.Connections().Create(context.Background(), repository.ConnInput{Provider: provider, AuthType: "oauth", Email: email, Fields: fields}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Reload reads a stored account again.
func (g *Renew) Reload(t *testing.T, id string) document.Doc {
	t.Helper()
	c, ok, err := g.St.Connections().Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("reload: %v %v", ok, err)
	}
	return c.Doc()
}

// FakeRegistry holds the two seed logins with their token address replaced.
func FakeRegistry(t *testing.T, tokenURL string) *upstream.Registry {
	return FakeRegistryPort(t, tokenURL, 1455)
}

// FakeRegistryPort is FakeRegistry with a chosen fixed callback port.
func FakeRegistryPort(t *testing.T, tokenURL string, port int) *upstream.Registry {
	t.Helper()
	entry := func(id, format, extra string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"id":"` + id + `","category":"oauth","transport":{"baseUrl":"https://x.example/v1","format":"` + format + `"},"models":[],` +
			`"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/authorize","tokenUrl":"` + tokenURL + `","scope":"s",` + extra + `}}`)}
	}
	reg, err := upstream.Load(fstest.MapFS{
		"data/claude.json": entry("claude", "claude", `"redirect":"local","exchange":"json","echoState":true,"refresh":{"encoding":"json","leadSeconds":14400}`),
		"data/codex.json":  entry("codex", "openai-responses", `"redirect":"fixed","fixedPort":`+strconv.Itoa(port)+`,"callbackPath":"/auth/callback","exchange":"form","hook":"codex","refresh":{"encoding":"json","leadSeconds":600,"maxAgeSeconds":691200}`),
		"data/openai.json": {Data: []byte(`{"id":"openai","category":"apikey","transport":{"baseUrl":"https://x.example/v1"},"models":[]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}
