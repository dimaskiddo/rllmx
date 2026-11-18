package controllers

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractshttp "github.com/goravel/framework/contracts/http"
	goravelgin "github.com/goravel/gin"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	"rllmx/app/access/quota"
	apphttp "rllmx/app/http"
	"rllmx/app/http/middleware"
	"rllmx/app/platform/cache"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// signedSession is an authenticator that accepts any dashboard session, so a
// route behind ClassAlwaysProtected answers as a signed-in operator.
type signedSession struct{}

func (signedSession) Session(*nethttp.Request) bool              { return true }
func (signedSession) APIKey(*nethttp.Request) identity.KeyResult { return identity.KeyResult{} }
func (signedSession) RequireLogin(context.Context) bool          { return true }
func (signedSession) RequireAPIKey(context.Context) bool         { return true }

// keysRig is the five routes wired to a migrated store.
type keysRig struct {
	route    *goravelgin.Route
	sq       *store.Store
	resolver *auth.Keys
	changed  int
}

func newKeysRig(t *testing.T, guardAuth apphttp.Authenticator) *keysRig {
	t.Helper()
	sq := store.Migrated(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "machine-id"), []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	rig := &keysRig{sq: sq}
	rig.resolver = auth.NewKeys(func() *repository.Store { return sq.Store },
		cache.New(tests.NewCache(), cache.WithoutShared()), identity.NewKeys(""))
	c := &KeysController{
		DB:      func() *repository.Store { return sq.Store },
		Quota:   quota.NewLimits(func() *repository.Store { return sq.Store }, nil),
		Codec:   identity.NewKeys(""),
		Machine: identity.NewMachine(""),
		Dir:     dir,
		Changed: func() { rig.changed++; rig.resolver.Changed() },
	}
	r := tests.NewRoute(t)
	g := apphttp.NewGuard(guardAuth, true)
	r.SetGlobalMiddleware([]contractshttp.Middleware{
		&middleware.RequestID{}, &middleware.SecurityHeaders{Trust: apphttp.NewTrust(nil)},
		&middleware.SameOrigin{}, &middleware.Guard{Guard: g},
	})
	g.Handle(r, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/keys", c.List)
	g.Handle(r, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/keys", c.Create)
	g.Handle(r, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/keys/{id}", c.Show)
	g.Handle(r, apphttp.ClassAlwaysProtected, nethttp.MethodPut, "/api/keys/{id}", c.Update)
	g.Handle(r, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/keys/{id}", c.Delete)
	rig.route = r
	return rig
}

func (k *keysRig) call(t *testing.T, method, path, body string, peer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = peer
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	k.route.ServeHTTP(rec, req)
	return rec
}

const (
	keysLocalPeer  = "127.0.0.1:5000"
	keysRemotePeer = "203.0.113.9:5000"
)

// create makes one key through the route and returns its id and value.
func (k *keysRig) create(t *testing.T, name string) (id, key string) {
	t.Helper()
	rec := k.call(t, nethttp.MethodPost, "/api/keys", `{"name":"`+name+`"}`, keysLocalPeer)
	if rec.Code != nethttp.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.ID, out.Key
}

// live asks the key resolver whether a key is accepted, which is what a /v1
// request would do. The resolver caches like the server's, so a revocation is
// only seen at once because a mutation purged the group.
func (k *keysRig) live(t *testing.T, key string) bool {
	t.Helper()
	return k.resolver.Lookup(t.Context(), key).Status == identity.KeyValid
}

func TestACreatedKeyIsUsableAndAnsweredOnce(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, key := rig.create(t, "laptop")
	if id == "" || key == "" {
		t.Fatal("no id or key")
	}
	if identity.NewKeys("").Classify(key) != identity.ClassOK {
		t.Fatalf("the key %q does not classify as one this product wrote", key)
	}
	if !rig.live(t, key) {
		t.Fatal("a fresh key is not accepted")
	}
	row, found, _ := rig.sq.APIKeys().GetByID(t.Context(), id)
	if !found || !row.IsActive || row.AccessRestricted || row.Name != "laptop" {
		t.Fatalf("%+v", row)
	}
	if rig.changed == 0 {
		t.Fatal("creating a key did not refresh the key cache")
	}
}

func TestAKeyNameIsCheckedBeforeAnyRowIsWritten(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	for name, body := range map[string]string{
		"missing":   `{}`,
		"empty":     `{"name":"   "}`,
		"too long":  `{"name":"` + strings.Repeat("a", 65) + `"}`,
		"a control": `{"name":"a\u0007b"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := rig.call(t, nethttp.MethodPost, "/api/keys", body, keysLocalPeer)
			if rec.Code != nethttp.StatusBadRequest {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	rows, _ := rig.sq.APIKeys().List(t.Context())
	if len(rows) != 0 {
		t.Fatalf("a refused name left %d rows", len(rows))
	}
	if rec := rig.call(t, nethttp.MethodPost, "/api/keys", `{"name":"`+strings.Repeat("a", 64)+`"}`, keysLocalPeer); rec.Code != nethttp.StatusCreated {
		t.Fatalf("a 64-byte name: %d", rec.Code)
	}
}

func TestTheListNeverCarriesTheWholeKey(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	_, one := rig.create(t, "one")
	_, two := rig.create(t, "two")
	rec := rig.call(t, nethttp.MethodGet, "/api/keys", "", keysLocalPeer)
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	body := rec.Body.String()
	for _, key := range []string{one, two} {
		if strings.Contains(body, key) {
			t.Fatal("the list carried a whole key")
		}
	}
	var out struct {
		Keys []struct {
			Key string `json:"key"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Keys) != 2 {
		t.Fatalf("%v %v", err, out)
	}
	for _, k := range out.Keys {
		if !strings.Contains(k.Key, "…") {
			t.Fatalf("a list key %q is not masked", k.Key)
		}
	}
}

func TestOnlyTheRevealAnswersAWholeKey(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, key := rig.create(t, "one")
	rec := rig.call(t, nethttp.MethodGet, "/api/keys/"+id, "", keysLocalPeer)
	if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), key) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rec := rig.call(t, nethttp.MethodGet, "/api/keys/does-not-exist", "", keysLocalPeer); rec.Code != nethttp.StatusNotFound {
		t.Fatalf("an unknown id: %d", rec.Code)
	}
}

func TestNoKeyRouteAnswersWithoutASessionOrFromAStranger(t *testing.T) {
	// No session at all: ClassAlwaysProtected refuses even when requireLogin is off.
	rig := newKeysRig(t, nil)
	for _, rt := range [][2]string{{"GET", "/api/keys"}, {"POST", "/api/keys"}, {"GET", "/api/keys/x"}, {"PUT", "/api/keys/x"}, {"DELETE", "/api/keys/x"}} {
		if rec := rig.call(t, rt[0], rt[1], "{}", keysLocalPeer); rec.Code != nethttp.StatusUnauthorized {
			t.Errorf("%s %s: %d", rt[0], rt[1], rec.Code)
		}
	}
	// A dashboard session is the whole requirement: an operator signed in from a
	// public address may manage keys, and a public caller without a session is
	// refused above by the same rule.
	signed := newKeysRig(t, signedSession{})
	if rec := signed.call(t, nethttp.MethodGet, "/api/keys", "", keysRemotePeer); rec.Code != nethttp.StatusOK {
		t.Fatalf("a signed-in remote operator answered %d", rec.Code)
	}
}

func TestRenamingAndTogglingChangeOnlyWhatWasAsked(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, key := rig.create(t, "before")
	rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"name":"after"}`, keysLocalPeer)
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	row, _, _ := rig.sq.APIKeys().GetByID(t.Context(), id)
	if row.Name != "after" || !row.IsActive {
		t.Fatalf("%+v", row)
	}
	if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"name":"`+strings.Repeat("a", 65)+`"}`, keysLocalPeer); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("an over-long name: %d", rec.Code)
	}
	if !rig.live(t, key) {
		t.Fatal("a rename stopped the key working")
	}
}

func TestADisabledKeyStopsAtOnceAndComesBack(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, key := rig.create(t, "k")
	if !rig.live(t, key) {
		t.Fatal("the key does not work before it is disabled")
	}
	if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"isActive":false}`, keysLocalPeer); rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if rig.live(t, key) {
		t.Fatal("a disabled key is still accepted; the cache was not refreshed")
	}
	if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"isActive":true}`, keysLocalPeer); rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if !rig.live(t, key) {
		t.Fatal("an enabled key is refused")
	}
}

func TestADeletedKeyStopsAtOnce(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, key := rig.create(t, "k")
	// Look it up first, so the resolver holds the key in its cache: the delete
	// must purge it, or the key would still work for the cache's own lifetime.
	if !rig.live(t, key) {
		t.Fatal("the key does not work before it is deleted")
	}
	rec := rig.call(t, nethttp.MethodDelete, "/api/keys/"+id, "", keysLocalPeer)
	if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), "Key deleted successfully") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if rig.live(t, key) {
		t.Fatal("a deleted key is still accepted")
	}
	if rec := rig.call(t, nethttp.MethodDelete, "/api/keys/"+id, "", keysLocalPeer); rec.Code != nethttp.StatusNotFound {
		t.Fatalf("deleting twice: %d", rec.Code)
	}
}

func TestAnAllowListRoundTripsAndIsRefusedWhenWrong(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, _ := rig.create(t, "k")
	rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"access":{"restricted":true,"allow":["openai/gpt-4o","claude/claude-opus-5"]}}`, keysLocalPeer)
	if rec.Code != nethttp.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	row, _, _ := rig.sq.APIKeys().GetByID(t.Context(), id)
	if !row.AccessRestricted || len(row.AccessAllow) != 2 {
		t.Fatalf("%+v", row)
	}
	for name, body := range map[string]string{
		"a non-boolean":     `{"access":{"restricted":"yes"}}`,
		"an unknown field":  `{"access":{"restricted":true,"allow":[],"x":1}}`,
		"an overlong entry": `{"access":{"restricted":true,"allow":["` + strings.Repeat("m", 257) + `"]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, body, keysLocalPeer); rec.Code != nethttp.StatusBadRequest {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAKeyBudgetRoundTripsAndIsRemovedWithTheKey(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, _ := rig.create(t, "k")
	rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"quota":{"requests":{"day":100},"cost":{"total":2.5}}}`, keysLocalPeer)
	if rec.Code != nethttp.StatusOK || !strings.Contains(rec.Body.String(), `"day":100`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if raw, ok, _ := rig.sq.KV().Get(t.Context(), repository.ScopeKeyLimits, id); !ok || !strings.Contains(raw, `"total":2.5`) {
		t.Fatalf("stored = %q %v", raw, ok)
	}
	// The listing carries it back.
	rec = rig.call(t, nethttp.MethodGet, "/api/keys", "", keysLocalPeer)
	if !strings.Contains(rec.Body.String(), `"quota":{"requests":{"day":100}`) {
		t.Fatalf("list = %s", rec.Body.String())
	}
	// An empty object clears it.
	rec = rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"quota":{}}`, keysLocalPeer)
	if rec.Code != nethttp.StatusOK || strings.Contains(rec.Body.String(), `"quota"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := rig.sq.KV().Get(t.Context(), repository.ScopeKeyLimits, id); ok {
		t.Fatal("the cleared budget left a row")
	}
	// A budget set again goes with the key.
	if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"quota":{"requests":{"day":1}}}`, keysLocalPeer); rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if rec := rig.call(t, nethttp.MethodDelete, "/api/keys/"+id, "", keysLocalPeer); rec.Code != nethttp.StatusOK {
		t.Fatalf("%d", rec.Code)
	}
	if _, ok, _ := rig.sq.KV().Get(t.Context(), repository.ScopeKeyLimits, id); ok {
		t.Fatal("the deleted key left its budget")
	}
}

func TestABadBudgetIsRefusedByName(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, _ := rig.create(t, "k")
	for name, body := range map[string]string{
		"a bad window":   `{"quota":{"requests":{"week":1}}}`,
		"a bad type":     `{"quota":{"images":{"day":1}}}`,
		"a negative day": `{"quota":{"requests":{"day":-1}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, body, keysLocalPeer)
			if rec.Code != nethttp.StatusBadRequest || !strings.Contains(strings.ToLower(rec.Body.String()), "quota") {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestTheCacheSwitchIsAKeyFieldAndTakesEffectAtOnce: the response cache is off
// for a key until the dashboard turns it on, and turning it off deletes the row
// rather than storing a false.
func TestTheCacheSwitchIsAKeyFieldAndTakesEffectAtOnce(t *testing.T) {
	k := newKeysRig(t, signedSession{})
	id, _ := k.create(t, "a")
	off, err := k.sq.Store.KV().ResponseCacheEnabled(context.Background(), id)
	if err != nil || off {
		t.Fatalf("a new key is on: %v %v", off, err)
	}
	rec := k.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"responseCache":true}`, keysLocalPeer)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"responseCache":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if on, _ := k.sq.Store.KV().ResponseCacheEnabled(context.Background(), id); !on {
		t.Fatal("the switch did not reach the store")
	}
	rec = k.call(t, nethttp.MethodPut, "/api/keys/"+id, `{"responseCache":false}`, keysLocalPeer)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), `"responseCache":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if row, found, _ := k.sq.Store.KV().Get(context.Background(), repository.ScopeResponseCache, id); found {
		t.Fatalf("the row survived: %q", row)
	}
}

func TestABadBudgetLeavesTheRestOfTheUpdateUnapplied(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	id, _ := rig.create(t, "before")
	body := `{"name":"after","isActive":false,"access":{"restricted":true,"allow":["openai/gpt-4o"]},"quota":{"requests":{"week":1}}}`
	if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+id, body, keysLocalPeer); rec.Code != nethttp.StatusBadRequest {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	row, _, _ := rig.sq.APIKeys().GetByID(t.Context(), id)
	if row.Name != "before" || !row.IsActive || row.AccessRestricted {
		t.Fatalf("a refused update changed the key: %+v", row)
	}
}

func TestTheListShowsEachKeysCacheSwitch(t *testing.T) {
	rig := newKeysRig(t, signedSession{})
	on, _ := rig.create(t, "on")
	off, _ := rig.create(t, "off")
	if rec := rig.call(t, nethttp.MethodPut, "/api/keys/"+on, `{"responseCache":true}`, keysLocalPeer); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	rec := rig.call(t, nethttp.MethodGet, "/api/keys", "", keysLocalPeer)
	var got struct {
		Keys []struct {
			ID            string `json:"id"`
			ResponseCache bool   `json:"responseCache"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, k := range got.Keys {
		seen[k.ID] = k.ResponseCache
	}
	if !seen[on] || seen[off] || len(seen) != 2 {
		t.Fatalf("%v", seen)
	}
}
