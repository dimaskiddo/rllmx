package controllers

import (
	"context"
	"encoding/json"
	"github.com/tidwall/gjson"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/route"
	goravelgin "github.com/goravel/gin"

	"rllmx/app/access/auth"
	"rllmx/app/access/identity"
	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/network"
	"rllmx/app/store/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

// guardMW installs the guard the way the application does.
type guardMW struct{ g *apphttp.Guard }

func (guardMW) Signature() string                  { return "test:guard" }
func (m guardMW) Handle(ctx contractshttp.Context) { m.g.Protect(ctx) }

// mgmtRig is the provider and node routes behind the real guard, with a
// signed-in session.
type mgmtRig struct {
	pc     *ProvidersController
	route  nethttp.Handler
	router route.Router
	auth   *auth.Authenticator
	st     *store.Store
	cookie string
	guard  *apphttp.Guard
}

func newMgmtRig(t *testing.T) *mgmtRig {
	t.Helper()
	st := store.Migrated(t)
	reg, err := upstream.Default()
	if err != nil {
		t.Fatal(err)
	}
	db := func() *repository.Store { return st.Store }
	codec := auth.NewKeys(db, nil, identity.NewKeys(""))
	secret := []byte("0123456789abcdef0123456789abcdef")
	a := auth.NewAuthenticator(auth.NewStore(db), codec, bcryptOnly{}, "", func() []byte { return secret }, true, false)
	if err := st.Settings().Patch(context.Background(), map[string]string{"requireLogin": "true"}); err != nil {
		t.Fatal(err)
	}
	g := apphttp.NewGuard(a, true)
	// The driver logs a body it cannot parse through its package-level facade,
	// which the application sets and a bare test does not.
	goravelgin.LogFacade = &tests.Log{}
	t.Cleanup(func() { goravelgin.LogFacade = nil })
	r := tests.NewRoute(t)
	// The guard is the middleware the application installs: without it these
	// routes would answer anyone.
	r.SetGlobalMiddleware([]contractshttp.Middleware{guardMW{g}})
	allow, aerr := network.ParseAllow("127.0.0.1,localhost")
	if aerr != nil {
		t.Fatal(aerr)
	}
	ck := NewChecker(allow)
	pc := &ProvidersController{Registry: reg, DB: db, Checker: ck}
	nc := &NodesController{Registry: reg, DB: db, Checker: ck}
	handle := func(method, path string, h contractshttp.HandlerFunc) {
		g.Handle(r, apphttp.ClassProtected, method, path, h)
	}
	handle(nethttp.MethodGet, "/api/providers", pc.List)
	handle(nethttp.MethodPost, "/api/providers", pc.Create)
	handle(nethttp.MethodPost, "/api/providers/validate", pc.Validate)
	handle(nethttp.MethodPost, "/api/providers/test-batch", pc.TestBatch)
	handle(nethttp.MethodPost, "/api/providers/bulk", pc.BulkCreate)
	handle(nethttp.MethodGet, "/api/providers/catalog", pc.Catalog)
	handle(nethttp.MethodGet, "/api/providers/{id}", pc.Show)
	handle(nethttp.MethodPut, "/api/providers/{id}", pc.Update)
	handle(nethttp.MethodDelete, "/api/providers/{id}", pc.Delete)
	handle(nethttp.MethodPost, "/api/providers/{id}/test", pc.Test)
	handle(nethttp.MethodGet, "/api/providers/{id}/models", pc.ModelsList)
	handle(nethttp.MethodPost, "/api/providers/{id}/test-models", pc.TestModels)
	handle(nethttp.MethodGet, "/api/providers/{id}/overrides", pc.Overrides)
	handle(nethttp.MethodPut, "/api/providers/{id}/overrides", pc.SetOverrides)
	handle(nethttp.MethodGet, "/api/provider-nodes", nc.List)
	handle(nethttp.MethodPost, "/api/provider-nodes", nc.Create)
	handle(nethttp.MethodPost, "/api/provider-nodes/validate", nc.Validate)
	handle(nethttp.MethodPut, "/api/provider-nodes/{id}", nc.Update)
	handle(nethttp.MethodDelete, "/api/provider-nodes/{id}", nc.Delete)

	cookie, ok := a.Issue(context.Background(), auth.MethodPassword, "", "", false)
	if !ok {
		t.Fatal("no session")
	}
	return &mgmtRig{route: r, router: r, auth: a, st: st, cookie: cookie.Value, guard: g, pc: pc}
}

func (m *mgmtRig) call(method, path, body string, signedIn bool) (int, map[string]any, string) {
	var req *nethttp.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr, req.Host = localPeer, localHost
	if signedIn {
		req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: m.cookie})
	}
	rec := httptest.NewRecorder()
	m.route.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (m *mgmtRig) ok(t *testing.T, method, path, body string, want int) map[string]any {
	t.Helper()
	code, out, raw := m.call(method, path, body, true)
	if code != want {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, code, want, raw)
	}
	return out
}

func connID(t *testing.T, out map[string]any) string {
	t.Helper()
	c, _ := out["connection"].(map[string]any)
	id, _ := c["id"].(string)
	if id == "" {
		t.Fatalf("no connection id in %v", out)
	}
	return id
}

func TestEveryManagementRouteNeedsASession(t *testing.T) {
	m := newMgmtRig(t)
	for _, rt := range [][2]string{
		{"GET", "/api/providers"}, {"POST", "/api/providers"}, {"GET", "/api/providers/x"}, {"PUT", "/api/providers/x"},
		{"DELETE", "/api/providers/x"}, {"GET", "/api/providers/x/overrides"}, {"PUT", "/api/providers/x/overrides"},
		{"GET", "/api/provider-nodes"}, {"POST", "/api/provider-nodes"}, {"PUT", "/api/provider-nodes/x"}, {"DELETE", "/api/provider-nodes/x"},
		{"POST", "/api/providers/validate"}, {"POST", "/api/providers/x/test"}, {"GET", "/api/providers/x/models"}, {"POST", "/api/provider-nodes/validate"},
	} {
		if code, _, raw := m.call(rt[0], rt[1], `{}`, false); code != nethttp.StatusUnauthorized {
			t.Errorf("%s %s without a session: %d %s", rt[0], rt[1], code, raw)
		}
	}
}

func TestCreateListUpdateDeleteNeverShowsACredential(t *testing.T) {
	m := newMgmtRig(t)
	out := m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"sk-top-secret-111","name":"main","providerSpecificData":{"region":"eu","secretHeader":"hide-me","nested":{"authToken":"hide-too"}}}`, 201)
	id := connID(t, out)
	for _, path := range []string{"/api/providers", "/api/providers/" + id} {
		_, _, raw := m.call("GET", path, "", true)
		for _, leak := range []string{"sk-top-secret-111", "hide-me", "hide-too"} {
			if strings.Contains(raw, leak) {
				t.Fatalf("%s leaks %q: %s", path, leak, raw)
			}
		}
	}
	_, _, raw := m.call("GET", "/api/providers/"+id, "", true)
	if !strings.Contains(raw, `"region":"eu"`) || !strings.Contains(raw, `"secretHeader":"***"`) {
		t.Fatalf("masking or pass-through wrong: %s", raw)
	}
	stored, _, _ := m.st.Connections().Get(context.Background(), id)
	if k, _ := stored.Doc().GetString("apiKey"); k != "sk-top-secret-111" {
		t.Fatalf("the key was not stored: %q", k)
	}

	m.ok(t, "PUT", "/api/providers/"+id, `{"name":"renamed","isActive":false}`, 200)
	got := m.ok(t, "GET", "/api/providers/"+id, "", 200)["connection"].(map[string]any)
	if got["name"] != "renamed" || got["isActive"] != false {
		t.Fatalf("%v", got)
	}
	m.ok(t, "DELETE", "/api/providers/"+id, "", 200)
	if code, _, _ := m.call("GET", "/api/providers/"+id, "", true); code != 404 {
		t.Fatalf("a deleted connection is still there: %d", code)
	}
	if code, _, _ := m.call("DELETE", "/api/providers/"+id, "", true); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
}

func TestAGitHubAccountNeverShowsItsLoginOrCopilotToken(t *testing.T) {
	m := newMgmtRig(t)
	c, err := m.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "github", AuthType: "oauth", Email: "o@x.test",
		Fields: map[string]string{"accessToken": `"GH-LOGIN-TOKEN"`, "testStatus": `"active"`},
		PSD:    `{"copilotToken":"tid=COPILOT-SECRET","copilotTokenExpiresAt":4102444800,"githubLogin":"octo","githubUserId":"4242"}`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/providers", "/api/providers/" + c.ID} {
		_, _, raw := m.call("GET", path, "", true)
		for _, leak := range []string{"GH-LOGIN-TOKEN", "COPILOT-SECRET"} {
			if strings.Contains(raw, leak) {
				t.Fatalf("%s leaks %q: %s", path, leak, raw)
			}
		}
		if !strings.Contains(raw, "octo") {
			t.Fatalf("%s hides the login name: %s", path, raw)
		}
	}
}

func TestNoCredentialFieldNameEverAppearsInAView(t *testing.T) {
	m := newMgmtRig(t)
	ctx := context.Background()
	conn, err := m.st.Connections().Create(ctx, repository.ConnInput{
		Provider: "claude", AuthType: "oauth", Email: "a@b.c",
		Fields: map[string]string{"accessToken": `"AT-111"`, "refreshToken": `"RT-222"`, "idToken": `"IT-333"`, "apiKey": `"AK-444"`, "futureField": `"FF-555"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, raw := m.call("GET", "/api/providers/"+conn.ID, "", true)
	for _, leak := range []string{"AT-111", "RT-222", "IT-333", "AK-444", "FF-555", "accessToken", "refreshToken", "idToken", "apiKey", "futureField"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("a view carries %q: %s", leak, raw)
		}
	}
}

func TestCreateRules(t *testing.T) {
	m := newMgmtRig(t)
	bad := map[string]string{
		"unknown provider":    `{"provider":"nope","apiKey":"k"}`,
		"oauth-only provider": `{"provider":"claude","apiKey":"k"}`,
		"no key":              `{"provider":"openai","name":"x"}`,
		"blank key":           `{"provider":"openai","apiKey":"   "}`,
		"control in key":      "{\"provider\":\"openai\",\"apiKey\":\"a\\u0085b\"}",
		"psd not an object":   `{"provider":"openai","apiKey":"k","providerSpecificData":[1]}`,
		"psd too deep":        `{"provider":"openai","apiKey":"k","providerSpecificData":{"a":{"b":{"c":{"d":1}}}}}`,
		"control around name": "{\"provider\":\"openai\",\"apiKey\":\"k\",\"name\":\"\\u0085a\"}",
		"not json":            `nope`,
		"two values":          `{"provider":"openai","apiKey":"k"}{}`,
	}
	for name, body := range bad {
		if code, _, raw := m.call("POST", "/api/providers", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, raw := m.call("POST", "/api/providers", `{"provider":"cc","apiKey":"k"}`, true); code != 400 {
		t.Errorf("alias of an oauth provider: %d %s", code, raw)
	}
	out := m.ok(t, "POST", "/api/providers", `{"provider":"ds","apiKey":"k"}`, 201)
	if c := out["connection"].(map[string]any); c["provider"] != "deepseek" || c["name"] != "DeepSeek" {
		t.Fatalf("alias not normalized or name not defaulted: %v", c)
	}
}

func TestADuplicateNameIs409UnlessOverwriteIsAsked(t *testing.T) {
	m := newMgmtRig(t)
	first := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"one","name":"dup"}`, 201))
	code, out, raw := m.call("POST", "/api/providers", `{"provider":"openai","apiKey":"two","name":"dup"}`, true)
	if code != 409 || out["code"] != "PROVIDER_NAME_CONFLICT" || out["existingId"] != first {
		t.Fatalf("%d %s", code, raw)
	}
	stored, _, _ := m.st.Connections().Get(context.Background(), first)
	if k, _ := stored.Doc().GetString("apiKey"); k != "one" {
		t.Fatalf("a refused create overwrote the key: %q", k)
	}
	m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"two","name":"dup","allowOverwrite":true}`, 201)
	stored, _, _ = m.st.Connections().Get(context.Background(), first)
	if k, _ := stored.Doc().GetString("apiKey"); k != "two" {
		t.Fatalf("overwrite did not replace the key: %q", k)
	}
}

func TestUpdateRefusesWhatOnlyTheServerWrites(t *testing.T) {
	m := newMgmtRig(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201))
	before, _, _ := m.st.Connections().Get(context.Background(), id)
	// The list is written out here on purpose: iterating the controller's own
	// slice would drop a test together with the entry it covers.
	for _, field := range []string{
		"accessToken", "refreshToken", "idToken", "expiresAt", "expiresIn", "tokenType", "scope", "projectId",
		"testStatus", "lastError", "lastErrorAt", "lastTested", "errorCode", "rateLimitedUntil", "backoffLevel",
		"consecutiveUseCount", "lastUsedAt", "lastRefreshAt", "provider", "authType", "email", "id", "createdAt", "updatedAt",
	} {
		body := `{"` + field + `":"x"}`
		if code, _, raw := m.call("PUT", "/api/providers/"+id, body, true); code != 400 {
			t.Errorf("%s accepted: %d %s", field, code, raw)
		}
	}
	for _, field := range []string{"modelLock_gpt-4o", "modelLock___all"} {
		m.call("PUT", "/api/providers/"+id, `{"`+field+`":"2099-01-01T00:00:00.000Z"}`, true)
	}
	after, _, _ := m.st.Connections().Get(context.Background(), id)
	if before.Data != after.Data {
		t.Fatalf("a refused or ignored update changed the row:\n%s\n%s", before.Data, after.Data)
	}
}

func TestAnEmptyKeyKeepsTheSavedOneAndANewKeyReplacesIt(t *testing.T) {
	m := newMgmtRig(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"saved","name":"a"}`, 201))
	m.ok(t, "PUT", "/api/providers/"+id, `{"apiKey":""}`, 200)
	c, _, _ := m.st.Connections().Get(context.Background(), id)
	if k, _ := c.Doc().GetString("apiKey"); k != "saved" {
		t.Fatalf("%q", k)
	}
	m.ok(t, "PUT", "/api/providers/"+id, `{"apiKey":"fresh"}`, 200)
	c, _, _ = m.st.Connections().Get(context.Background(), id)
	if k, _ := c.Doc().GetString("apiKey"); k != "fresh" {
		t.Fatalf("%q", k)
	}
}

func TestProviderSpecificDataMergesAndNullRemoves(t *testing.T) {
	m := newMgmtRig(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a","providerSpecificData":{"keep":1,"drop":2}}`, 201))
	m.ok(t, "PUT", "/api/providers/"+id, `{"providerSpecificData":{"drop":null,"added":3}}`, 200)
	c, _, _ := m.st.Connections().Get(context.Background(), id)
	raw, _ := c.Doc().GetRaw("providerSpecificData")
	if !strings.Contains(raw, `"keep":1`) || !strings.Contains(raw, `"added":3`) || strings.Contains(raw, "drop") {
		t.Fatalf("%s", raw)
	}
}

func TestTogglingActiveLeavesTheCooldownAlone(t *testing.T) {
	// Only a server-side success clears a lock; switching an account off and on
	// must not hide a real cooldown from the selector.
	m := newMgmtRig(t)
	ctx := context.Background()
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201))
	if _, err := m.st.Connections().LockFromLevel(ctx, id, "m", 429, "r", func(int) (time.Time, int, bool) { return time.Now().Add(time.Hour), 3, true }); err != nil {
		t.Fatal(err)
	}
	m.ok(t, "PUT", "/api/providers/"+id, `{"isActive":false}`, 200)
	m.ok(t, "PUT", "/api/providers/"+id, `{"isActive":true}`, 200)
	c, _, _ := m.st.Connections().Get(ctx, id)
	if lvl, _ := c.Doc().GetInt("backoffLevel"); lvl != 3 {
		t.Errorf("backoff level %d: toggling cleared the cooldown", lvl)
	}
	if v, _ := c.Doc().GetString("modelLock_m"); v == "" {
		t.Errorf("the lock was cleared: %s", c.Data)
	}
}

func TestOverridesValidation(t *testing.T) {
	m := newMgmtRig(t)
	tooMany := map[string]string{}
	for i := 0; i < 21; i++ {
		tooMany["X-H"+string(rune('a'+i))] = "v"
	}
	manyJSON, _ := json.Marshal(map[string]any{"headers": tooMany})
	bad := map[string]string{
		"authorization":    `{"headers":{"Authorization":"Bearer x"}}`,
		"cookie lower":     `{"headers":{"cookie":"a=b"}}`,
		"host":             `{"headers":{"Host":"evil"}}`,
		"content-type":     `{"headers":{"Content-Type":"x"}}`,
		"transfer":         `{"headers":{"Transfer-Encoding":"chunked"}}`,
		"bad name":         `{"headers":{"X Bad":"v"}}`,
		"underscore name":  `{"headers":{"X_Bad":"v"}}`,
		"crlf in value":    `{"headers":{"X-A":"a\r\nX-B: b"}}`,
		"C1 in value":      "{\"headers\":{\"X-A\":\"a\\u0085b\"}}",
		"nul in value":     `{"headers":{"X-A":"a\u0000b"}}`,
		"non string value": `{"headers":{"X-A":5}}`,
		"too long":         `{"headers":{"X-A":"` + strings.Repeat("a", 8193) + `"}}`,
		"too many":         string(manyJSON),
	}
	for name, body := range bad {
		if code, _, raw := m.call("PUT", "/api/providers/openai/overrides", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, _ := m.call("PUT", "/api/providers/no-such-provider/overrides", `{"headers":{"X-A":"v"}}`, true); code != 404 {
		t.Errorf("unknown provider: %d", code)
	}
	if code, _, _ := m.call("GET", "/api/providers/no-such-provider/overrides", "", true); code != 404 {
		t.Errorf("unknown provider read: %d", code)
	}
	set, _ := m.st.Settings().Get(context.Background())
	if set.Has("providerOverrides") {
		t.Fatalf("a refused override wrote the settings row: %s", set.String())
	}
}

// TestTheOverridesRouteOffersTheProvidersOwnHeaders covers the card's
// prefill: a login provider's identity headers and its api version, and nothing
// for a provider node, which has no defaults of its own.
func TestTheOverridesRouteOffersTheProvidersOwnHeaders(t *testing.T) {
	m := newMgmtRig(t)
	got := m.ok(t, "GET", "/api/providers/claude/overrides", "", 200)
	builtin, _ := got["builtinHeaders"].(map[string]any)
	if builtin["anthropic-version"] != upstream.ClaudeVersion || builtin["X-App"] != "cli" {
		t.Fatalf("claude defaults %v", got)
	}
	if ua, _ := builtin["User-Agent"].(string); !strings.HasPrefix(ua, "claude-cli/") {
		t.Fatalf("user agent %v", builtin["User-Agent"])
	}
	if _, ok := builtin["anthropic-beta"]; ok {
		t.Fatal("the beta list is computed per request, not offered as a default")
	}
	if _, ok := builtin["Authorization"]; ok {
		t.Fatalf("a credential was offered: %v", builtin)
	}

	id := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"My Node","prefix":"mine","baseUrl":"https://x.example","type":"anthropic-compatible","apiType":"chat"}`, 201))
	if got := m.ok(t, "GET", "/api/providers/"+id+"/overrides", "", 200); len(got["builtinHeaders"].(map[string]any)) != 0 {
		t.Fatalf("a node has no defaults: %v", got)
	}
}

func TestOverridesAreKeyedByTheCanonicalIDAndKeepOtherProviders(t *testing.T) {
	m := newMgmtRig(t)
	m.ok(t, "PUT", "/api/providers/cc/overrides", `{"headers":{"X-One":"1"}}`, 200)
	m.ok(t, "PUT", "/api/providers/openai/overrides", `{"headers":{"X-Two":"2"}}`, 200)
	got := m.ok(t, "GET", "/api/providers/claude/overrides", "", 200)
	if h, _ := got["headers"].(map[string]any); h["X-One"] != "1" {
		t.Fatalf("the alias and the id are not the same key: %v", got)
	}
	if got := m.ok(t, "GET", "/api/providers/openai/overrides", "", 200); got["headers"].(map[string]any)["X-Two"] != "2" {
		t.Fatalf("a second provider's write replaced the first: %v", got)
	}
	m.ok(t, "PUT", "/api/providers/claude/overrides", `{"headers":{}}`, 200)
	if got := m.ok(t, "GET", "/api/providers/claude/overrides", "", 200); len(got["headers"].(map[string]any)) != 0 {
		t.Fatalf("an empty payload did not clear: %v", got)
	}
	if got := m.ok(t, "GET", "/api/providers/openai/overrides", "", 200); got["headers"].(map[string]any)["X-Two"] != "2" {
		t.Fatalf("clearing one provider cleared another: %v", got)
	}
}

// pointRegistryAt swaps one provider's registry entry for a copy whose address
// is a local test server, so a test can see whether anything dials it.
func (m *mgmtRig) pointRegistryAt(t *testing.T, id, url string) {
	t.Helper()
	reg, err := upstream.Load(testRegistryFS(id, url), "data")
	if err != nil {
		t.Fatal(err)
	}
	m.pc.Registry = reg
}

func TestAMaskedValueSentBackNeverReplacesAStoredSecret(t *testing.T) {
	m := newMgmtRig(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"opendesign-go","apiKey":"k","name":"a","providerSpecificData":{"opendesignControlKey":"vela_ctrl_REAL","opendesignWorkspaceId":"ws-1"}}`, 201))
	m.ok(t, "PUT", "/api/providers/"+id, `{"providerSpecificData":{"opendesignControlKey":"***","opendesignWorkspaceId":"ws-2"}}`, 200)
	c, _, _ := m.st.Connections().Get(context.Background(), id)
	raw, _ := c.Doc().GetRaw("providerSpecificData")
	if gjson.Get(raw, "opendesignControlKey").String() != "vela_ctrl_REAL" {
		t.Fatalf("a masked value overwrote the secret: %s", raw)
	}
	if gjson.Get(raw, "opendesignWorkspaceId").String() != "ws-2" {
		t.Fatalf("the rest of the update was dropped: %s", raw)
	}
	m.ok(t, "PUT", "/api/providers/"+id, `{"providerSpecificData":{"opendesignControlKey":"vela_ctrl_NEW"}}`, 200)
	c, _, _ = m.st.Connections().Get(context.Background(), id)
	raw, _ = c.Doc().GetRaw("providerSpecificData")
	if gjson.Get(raw, "opendesignControlKey").String() != "vela_ctrl_NEW" {
		t.Fatalf("a real new value was refused: %s", raw)
	}
	for _, path := range []string{"/api/providers", "/api/providers/" + id} {
		if _, _, body := m.call("GET", path, "", true); strings.Contains(body, "vela_ctrl_") {
			t.Fatalf("%s shows the control key: %s", path, body)
		}
	}
}
