package controllers

import (
	"encoding/json"
	nethttp "net/http"
	"strings"
	"testing"
	"testing/fstest"

	"rllmx/app/llm/oauth"
	"rllmx/app/llm/oauth/dialects"
	"rllmx/app/llm/upstream"
)

// catalogFixture builds a registry with an API-key entry full of internal
// addresses and a device login, so a leak shows up as a substring.
func catalogFixture(t *testing.T) *upstream.Registry {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{
		"data/keyed.json": {Data: []byte(`{"id":"keyed","alias":"kd","category":"apikey","display":{"name":"Keyed"},
			"transport":{"baseUrl":"https://transport.example/v1","format":"openai","headers":{"x-trace":"1"},
			"regions":[{"id":"us","host":"https://us.internal.example"},{"id":"eu","host":"https://eu.internal.example"}],"defaultRegion":"us"},
			"auth":{"scheme":"bearer","header":"Authorization","validateUrl":"https://transport.example/check-key"},
			"models":[]}`)},
		"data/login.json": {Data: []byte(`{"id":"login","category":"oauth","display":{"name":"Login"},
			"transport":{"baseUrl":"https://transport.example/v1","format":"openai"},
			"oauth":{"flow":"device","deviceUrl":"https://device.example/start","tokenUrl":"https://token.example/",
			"scope":"read:all write:all","refresh":{"encoding":"json","leadSeconds":300}},"models":[]}`)},
		"data/imported.json": {Data: []byte(`{"id":"imported","category":"oauth","display":{"name":"Imported"},
			"transport":{"baseUrl":"https://transport.example/v1","format":"openai"},
			"oauth":{"flow":"import","tokenUrl":"https://token.example/","fields":["machineId"]},"models":[]}`)},
		"data/media.json": {Data: []byte(`{"id":"media","category":"apikey","display":{"name":"Media"},
			"transport":{"baseUrl":"https://transport.example/v1","format":"openai"},
			"media":{"serviceKinds":["llm","tts"],"hiddenKinds":["tts"],"ttsConfig":{"baseUrl":"https://transport.example/tts","authType":"none"}},
			"models":[]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func catalogBody(t *testing.T, c *ProvidersController) (int, string) {
	t.Helper()
	rec := serve(t, "/api/providers/catalog", c.Catalog, nethttp.MethodGet)
	return rec.Code, rec.Body.String()
}

type catalogLogin struct {
	Flow     string   `json:"flow"`
	Enabled  bool     `json:"enabled"`
	Variants []string `json:"variants"`
}

func decodeCatalog(t *testing.T, body string) []map[string]json.RawMessage {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &top); err != nil {
		t.Fatalf("catalog is not an object: %v (%s)", err, body)
	}
	if len(top) != 1 {
		t.Fatalf("catalog has keys %v", top)
	}
	var list []map[string]json.RawMessage
	if err := json.Unmarshal(top["providers"], &list); err != nil {
		t.Fatalf("providers is not a list: %v", err)
	}
	return list
}

// Without an OAuth service the router cannot say whether a login would work, so
// the catalog offers none rather than a button it cannot stand behind.
func TestTheCatalogOffersNoLoginWithoutTheLoginService(t *testing.T) {
	code, body := catalogBody(t, &ProvidersController{Registry: catalogFixture(t)})
	if code != nethttp.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	for _, p := range decodeCatalog(t, body) {
		if _, ok := p["login"]; ok {
			t.Errorf("%s offers a login with no service behind it", p["id"])
		}
	}
}

// The registry is read once at start, so the catalog is built once and every
// later request is the same answer: a dashboard that opens the page, or a few
// people refreshing it, never walk the whole registry again.
func TestTheCatalogIsBuiltOnceAndServedIdentically(t *testing.T) {
	reg := catalogFixture(t)
	var asked int
	c := &ProvidersController{Registry: reg, OAuth: &oauth.Service{
		Registry: reg, Dialects: dialects.Default(),
		ClientID: func(string) string { asked++; return "id" },
	}}
	_, first := catalogBody(t, c)
	after := asked
	if after == 0 {
		t.Fatal("the first request read no client id; the counter is not watching the build")
	}
	for range 3 {
		if _, again := catalogBody(t, c); again != first {
			t.Fatalf("a later answer differs:\n%s\n%s", first, again)
		}
	}
	if asked != after {
		t.Fatalf("three more requests rebuilt the catalog: %d client-id reads, was %d", asked, after)
	}
}

func TestTheCatalogShowsOnlyPresentationalFields(t *testing.T) {
	reg := catalogFixture(t)
	c := &ProvidersController{Registry: reg, OAuth: &oauth.Service{Registry: reg, Dialects: dialects.Default(), ClientID: func(string) string { return "id" }}}
	code, body := catalogBody(t, c)
	if code != nethttp.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	list := decodeCatalog(t, body)
	if len(list) != 4 {
		t.Fatalf("got %d providers: %s", len(list), body)
	}
	allowed := map[string]bool{
		"id": true, "alias": true, "name": true, "category": true, "kinds": true,
		"regions": true, "defaultRegion": true, "login": true,
	}
	for _, p := range list {
		for k := range p {
			if !allowed[k] {
				t.Errorf("provider %s carries %q", p["id"], k)
			}
		}
	}
	for _, leak := range []string{
		"https://", "http://", "internal.example", "transport.example", "device.example",
		"token.example", "check-key", "read:all", "x-trace", "client",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("catalog leaks %q: %s", leak, body)
		}
	}

	byID := map[string]map[string]json.RawMessage{}
	for _, p := range list {
		var id string
		_ = json.Unmarshal(p["id"], &id)
		byID[id] = p
	}
	keyed := byID["keyed"]
	var regions []string
	_ = json.Unmarshal(keyed["regions"], &regions)
	if len(regions) != 2 || regions[0] != "us" || regions[1] != "eu" {
		t.Errorf("regions: %v", regions)
	}
	var kinds []string
	_ = json.Unmarshal(keyed["kinds"], &kinds)
	if len(kinds) != 1 || kinds[0] != upstream.KindLLM {
		t.Errorf("kinds: %v", kinds)
	}
	if _, ok := keyed["login"]; ok {
		t.Errorf("an API-key provider offers a login: %s", keyed["login"])
	}
	var name, category string
	_ = json.Unmarshal(keyed["name"], &name)
	_ = json.Unmarshal(keyed["category"], &category)
	if name != "Keyed" || category != upstream.CategoryAPIKey {
		t.Errorf("name %q category %q", name, category)
	}
	var mediaKinds []string
	_ = json.Unmarshal(byID["media"]["kinds"], &mediaKinds)
	if len(mediaKinds) != 1 || mediaKinds[0] != upstream.KindLLM {
		t.Errorf("a hidden kind is offered: %v", mediaKinds)
	}
}

func TestTheCatalogSaysWhenALoginIsSwitchedOff(t *testing.T) {
	reg := catalogFixture(t)
	svc := &oauth.Service{Registry: reg, Dialects: dialects.Default()}
	for _, tc := range []struct {
		name     string
		clientID string
		want     bool
	}{
		{name: "configured", clientID: "cid", want: true},
		{name: "not configured", clientID: "", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc.ClientID = func(string) string { return tc.clientID }
			c := &ProvidersController{Registry: reg, OAuth: svc}
			code, body := catalogBody(t, c)
			if code != nethttp.StatusOK {
				t.Fatalf("%d %s", code, body)
			}
			list := decodeCatalog(t, body)
			seen := 0
			for _, p := range list {
				id := string(p["id"])
				if id != `"login"` && id != `"imported"` {
					continue
				}
				seen++
				var login catalogLogin
				if err := json.Unmarshal(p["login"], &login); err != nil {
					t.Fatalf("%s login: %v", id, err)
				}
				want := tc.want
				if id == `"imported"` {
					want = true
				}
				if login.Enabled != want {
					t.Errorf("%s login enabled %v, want %v", id, login.Enabled, want)
				}
			}
			if seen != 2 {
				t.Fatalf("saw %d login providers", seen)
			}
		})
	}
}

func TestTheCatalogListsTheVariantsOfACodeLogin(t *testing.T) {
	reg, err := upstream.Load(fstest.MapFS{
		"data/social.json": {Data: []byte(`{"id":"social","category":"free","display":{"name":"Social"},
			"transport":{"baseUrl":"https://transport.example/v1","format":"openai"},
			"oauth":{"flow":"code-pkce","authorizeUrl":"https://a.example/","tokenUrl":"https://t.example/",
			"scope":"s","exchange":"form","dialect":"kiro-social","noClient":true,"redirect":"local",
			"refresh":{"encoding":"json","leadSeconds":300}},"models":[]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	c := &ProvidersController{Registry: reg, OAuth: &oauth.Service{Registry: reg, Dialects: dialects.Default()}}
	code, body := catalogBody(t, c)
	if code != nethttp.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	list := decodeCatalog(t, body)
	if len(list) != 1 {
		t.Fatalf("%s", body)
	}
	var login catalogLogin
	if err := json.Unmarshal(list[0]["login"], &login); err != nil {
		t.Fatal(err)
	}
	if len(login.Variants) != 2 || login.Variants[0] != upstream.VariantGoogle || login.Variants[1] != upstream.VariantGitHub {
		t.Fatalf("variants %v", login.Variants)
	}
	if !login.Enabled {
		t.Fatal("a login with no client id and noClient is switched off")
	}
}

func TestTheCatalogIsSortedByName(t *testing.T) {
	reg, err := upstream.Load(fstest.MapFS{
		"data/zzz.json": {Data: []byte(`{"id":"zzz","category":"apikey","display":{"name":"Aardvark"},"transport":{"baseUrl":"https://x.example/v1","format":"openai"},"models":[]}`)},
		"data/aaa.json": {Data: []byte(`{"id":"aaa","category":"apikey","display":{"name":"Zebra"},"transport":{"baseUrl":"https://x.example/v1","format":"openai"},"models":[]}`)},
		"data/mmm.json": {Data: []byte(`{"id":"mmm","category":"apikey","display":{"name":"Mule"},"transport":{"baseUrl":"https://x.example/v1","format":"openai"},"models":[]}`)},
	}, "data")
	if err != nil {
		t.Fatal(err)
	}
	code, body := catalogBody(t, &ProvidersController{Registry: reg})
	if code != nethttp.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	list := decodeCatalog(t, body)
	want := []string{"Aardvark", "Mule", "Zebra"}
	for i, name := range want {
		var got string
		_ = json.Unmarshal(list[i]["name"], &got)
		if got != name {
			t.Fatalf("position %d is %q, want %q (%s)", i, got, name, body)
		}
	}
}

func TestTheCatalogIsNotReadAsAConnectionID(t *testing.T) {
	m := newMgmtRig(t)
	code, out, raw := m.call("GET", "/api/providers/catalog", "", true)
	if code != nethttp.StatusOK {
		t.Fatalf("%d %s", code, raw)
	}
	if _, ok := out["providers"]; !ok {
		t.Fatalf("catalog answered without a provider list: %s", raw)
	}
	if code, _, _ := m.call("GET", "/api/providers/no-such-connection", "", true); code != 404 {
		t.Fatalf("a connection id answered %d beside the catalog", code)
	}
	if code, _, _ := m.call("GET", "/api/providers/catalog", "", false); code != 401 {
		t.Fatalf("the catalog answers an anonymous caller: %d", code)
	}
}
