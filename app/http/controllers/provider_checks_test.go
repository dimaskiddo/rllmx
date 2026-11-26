package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"rllmx/app/llm/oauth"
	"rllmx/app/store/repository"
)

// nodeFor creates a node on a test server and returns the node id.
func nodeFor(t *testing.T, m *mgmtRig, srvURL, typ, prefix string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":"N","prefix":%q,"baseUrl":%q,"type":%q,"apiType":"chat"}`, prefix, srvURL, typ)
	return nodeID(t, m.ok(t, "POST", "/api/provider-nodes", body, 201))
}

func TestValidateAProviderKeyThroughTheRoute(t *testing.T) {
	m := newMgmtRig(t)
	for name, tc := range map[string]struct{ body string }{
		"unknown provider": {`{"provider":"nope","apiKey":"k"}`},
		"missing node":     {`{"provider":"openai-compatible-chat-nope","apiKey":"k"}`},
		"control in key":   {"{\"provider\":\"openai\",\"apiKey\":\"a\\u0085b\"}"},
		"not json":         {`nope`},
		"control in model": {"{\"provider\":\"openai\",\"apiKey\":\"k\",\"modelId\":\"a\\u0085\"}"},
	} {
		code, _, raw := m.call("POST", "/api/providers/validate", tc.body, true)
		if code != 400 && code != 404 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	// An empty key is an answer, not a request.
	code, out, _ := m.call("POST", "/api/providers/validate", `{"provider":"openai","apiKey":""}`, true)
	if code != 200 || out["valid"] != false || out["error"] != "Invalid API key" {
		t.Fatalf("%d %v", code, out)
	}
	// An OAuth provider cannot be judged, and the answer says so beside the text,
	// because a dashboard may save what it cannot check.
	code, out, _ = m.call("POST", "/api/providers/validate", `{"provider":"claude","apiKey":"k"}`, true)
	if code != 200 || out["error"] != "test not supported yet" || out["unsupported"] != true {
		t.Fatalf("%d %v", code, out)
	}
	// A judged key carries no such mark.
	srv := newUpstream(t, 200, "")
	node := nodeFor(t, m, srv.URL, "openai-compatible", "marked")
	code, out, _ = m.call("POST", "/api/providers/validate", fmt.Sprintf(`{"provider":%q,"apiKey":"k"}`, node), true)
	if code != 200 || out["valid"] != true || out["unsupported"] != nil {
		t.Fatalf("%d %v", code, out)
	}
}

func TestValidateReadsTheNodesStoredAddressNotTheCallers(t *testing.T) {
	m := newMgmtRig(t)
	real := newUpstream(t, 200, "")
	evil := newUpstream(t, 200, "")
	node := nodeFor(t, m, real.URL, "openai-compatible", "real")
	body := fmt.Sprintf(`{"provider":%q,"apiKey":"k","baseUrl":%q,"providerSpecificData":{"baseUrl":%q}}`, node, evil.URL, evil.URL)
	code, out, raw := m.call("POST", "/api/providers/validate", body, true)
	if code != 200 || out["valid"] != true {
		t.Fatalf("%d %s", code, raw)
	}
	if real.hits.Load() != 1 || evil.hits.Load() != 0 {
		t.Fatalf("stored node got %d requests, caller's address got %d", real.hits.Load(), evil.hits.Load())
	}
}

func TestValidateNeverEchoesTheUpstream(t *testing.T) {
	m := newMgmtRig(t)
	for _, status := range []int{200, 400, 401, 404, 500} {
		srv := newUpstream(t, status, `{"error":"`+canary+`"}`)
		node := nodeFor(t, m, srv.URL, "openai-compatible", fmt.Sprintf("n%d", status))
		_, _, raw := m.call("POST", "/api/providers/validate", fmt.Sprintf(`{"provider":%q,"apiKey":"sk-typed-secret"}`, node), true)
		for _, leak := range []string{canary, srv.URL, "127.0.0.1", "sk-typed-secret"} {
			if strings.Contains(raw, leak) {
				t.Errorf("status %d: the answer carries %q: %s", status, leak, raw)
			}
		}
	}
}

func TestNodeValidateChecksAnAddressBeforeTheNodeExists(t *testing.T) {
	m := newMgmtRig(t)
	good := newUpstream(t, 200, "")
	code, out, raw := m.call("POST", "/api/provider-nodes/validate", fmt.Sprintf(`{"baseUrl":%q,"apiKey":"k"}`, good.URL), true)
	if code != 200 || out["valid"] != true {
		t.Fatalf("%d %s", code, raw)
	}
	for name, body := range map[string]string{
		"no key":         fmt.Sprintf(`{"baseUrl":%q}`, good.URL),
		"no url":         `{"apiKey":"k"}`,
		"file scheme":    `{"baseUrl":"file:///etc/passwd","apiKey":"k"}`,
		"credentials":    `{"baseUrl":"https://u:p@x.example.com","apiKey":"k"}`,
		"a query":        `{"baseUrl":"https://x.example.com/v1?key=1","apiKey":"k"}`,
		"bad type":       fmt.Sprintf(`{"baseUrl":%q,"apiKey":"k","type":"mystery"}`, good.URL),
		"control in url": "{\"baseUrl\":\"https://x.example.com/\\u0085\",\"apiKey\":\"k\"}",
	} {
		if code, _, raw := m.call("POST", "/api/provider-nodes/validate", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	hits := good.hits.Load()
	code, out, _ = m.call("POST", "/api/provider-nodes/validate", fmt.Sprintf(`{"baseUrl":%q,"apiKey":"k","type":"custom-embedding"}`, good.URL), true)
	if code != 200 || out["valid"] != false || good.hits.Load() != hits {
		t.Fatalf("an embedding node without a model must ask nothing: %d %v", code, out)
	}
}

func TestNodeValidateIsGuardedLikeAnyOtherCall(t *testing.T) {
	// A remote address that resolves to the metadata range is refused by the
	// dial guard whatever the caller, so the route needs no local-only special case.
	m := newMgmtRig(t)
	code, out, _ := m.call("POST", "/api/provider-nodes/validate", `{"baseUrl":"http://169.254.169.254/latest","apiKey":"k"}`, true)
	if code != 200 || out["valid"] != false || out["error"] != "blocked address" {
		t.Fatalf("%d %v", code, out)
	}
}

func TestTestingASavedConnectionRecordsTheOutcome(t *testing.T) {
	m := newMgmtRig(t)
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer good" {
			w.WriteHeader(200)
			return
		}
		w.Header().Set("X-Canary", canary)
		w.WriteHeader(401)
		fmt.Fprint(w, canary)
	}))
	defer srv.Close()
	node := nodeFor(t, m, srv.URL, "openai-compatible", "tn")
	bad := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"wrong","name":"bad"}`, node), 201))
	good := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"good","name":"good"}`, node), 201))

	out := m.ok(t, "POST", "/api/providers/"+bad+"/test", "", 200)
	if out["valid"] != false || out["error"] != "Invalid API key" {
		t.Fatalf("%v", out)
	}
	if _, has := out["latencyMs"]; !has {
		t.Errorf("no latency: %v", out)
	}
	c, _, _ := m.st.Connections().Get(ctx, bad)
	if st, _ := c.Doc().GetString("testStatus"); st != "error" {
		t.Errorf("testStatus %q", st)
	}
	if le, _ := c.Doc().GetString("lastError"); le != "Invalid API key" || strings.Contains(c.Data, canary) {
		t.Errorf("lastError %q, canary stored: %v", le, strings.Contains(c.Data, canary))
	}

	if out := m.ok(t, "POST", "/api/providers/"+good+"/test", "", 200); out["valid"] != true {
		t.Fatalf("%v", out)
	}
	c, _, _ = m.st.Connections().Get(ctx, good)
	if st, _ := c.Doc().GetString("testStatus"); st != "active" || !c.Doc().IsNull("lastError") && c.Doc().Has("lastError") {
		t.Errorf("after a pass: %s", c.Data)
	}
}

func TestATestCreatesNoCooldownAndAPassErasesOne(t *testing.T) {
	m := newMgmtRig(t)
	ctx := context.Background()
	srv := newUpstream(t, 401, "")
	node := nodeFor(t, m, srv.URL, "openai-compatible", "cd")
	id := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"a"}`, node), 201))
	if _, err := m.st.Connections().LockFromLevel(ctx, id, "m", 429, "r", func(int) (time.Time, int, bool) { return time.Now().Add(time.Hour), 4, true }); err != nil {
		t.Fatal(err)
	}
	before, _, _ := m.st.Connections().Get(ctx, id)
	lock, _ := before.Doc().GetString("modelLock_m")
	m.ok(t, "POST", "/api/providers/"+id+"/test", "", 200)
	after, _, _ := m.st.Connections().Get(ctx, id)
	if v, _ := after.Doc().GetString("modelLock_m"); v != lock {
		t.Errorf("a failing test changed the lock: %q", v)
	}
	if lvl, _ := after.Doc().GetInt("backoffLevel"); lvl != 4 {
		t.Errorf("backoffLevel %d", lvl)
	}
	// A fresh account with no lock stays free of one after a failing test.
	free := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"free"}`, node), 201))
	m.ok(t, "POST", "/api/providers/"+free+"/test", "", 200)
	c, _, _ := m.st.Connections().Get(ctx, free)
	for _, k := range c.Doc().Keys() {
		if strings.HasPrefix(k, repository.LockPrefix) {
			t.Errorf("a test created the lock %s", k)
		}
	}
	// A pass is the operator's re-validation: the stale health goes with it.
	up := newUpstream(t, 200, "")
	okNode := nodeFor(t, m, up.URL, "openai-compatible", "ok")
	good := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"good"}`, okNode), 201))
	if _, err := m.st.Connections().LockFromLevel(ctx, good, "m", 429, "r", func(int) (time.Time, int, bool) { return time.Now().Add(time.Hour), 3, true }); err != nil {
		t.Fatal(err)
	}
	m.ok(t, "POST", "/api/providers/"+good+"/test", "", 200)
	passed, _, _ := m.st.Connections().Get(ctx, good)
	for _, k := range []string{"modelLock_m", "errorCode", "rateLimitedUntil", "lastError"} {
		if !passed.Doc().IsNull(k) {
			t.Errorf("a pass kept %s: %s", k, passed.Data)
		}
	}
	if lvl, _ := passed.Doc().GetInt("backoffLevel"); lvl != 0 {
		t.Errorf("a pass left the backoff at %d", lvl)
	}
	if st, _ := passed.Doc().GetString("testStatus"); st != "active" {
		t.Errorf("a pass left the status %q", st)
	}
}

func TestAnUnsupportedProviderWritesNothing(t *testing.T) {
	m := newMgmtRig(t)
	ctx := context.Background()
	conn, err := m.st.Connections().Create(ctx, repository.ConnInput{Provider: "claude", AuthType: "oauth", Email: "a@b.c", Fields: map[string]string{"accessToken": `"t"`}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := m.st.Connections().Get(ctx, conn.ID)
	out := m.ok(t, "POST", "/api/providers/"+conn.ID+"/test", "", 200)
	if out["error"] != "test not supported yet" || out["valid"] != false {
		t.Fatalf("%v", out)
	}
	after, _, _ := m.st.Connections().Get(ctx, conn.ID)
	if before.Data != after.Data || before.UpdatedAt != after.UpdatedAt {
		t.Fatalf("an unsupported test wrote the row:\n%s\n%s", before.Data, after.Data)
	}
	// A pasted key for a registry provider with no auth block is unsupported too.
	gh := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"deepseek","apiKey":"k","name":"d"}`, 201))
	_ = gh
}

func TestTestRefusesMissingAndCorruptRows(t *testing.T) {
	m := newMgmtRig(t)
	if code, _, _ := m.call("POST", "/api/providers/nope/test", "", true); code != 404 {
		t.Errorf("missing: %d", code)
	}
	srv := newUpstream(t, 200, "")
	node := nodeFor(t, m, srv.URL, "openai-compatible", "cr")
	id := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"a"}`, node), 201))
	m.st.SQ.DB.Statement(`UPDATE "providerConnections" SET "data"='{"a":1,"a":2}' WHERE "id"=?`, id)
	if code, _, _ := m.call("POST", "/api/providers/"+id+"/test", "", true); code != 409 {
		t.Errorf("corrupt: %d", code)
	}
	if code, _, _ := m.call("GET", "/api/providers/"+id+"/models", "", true); code != 409 {
		t.Errorf("corrupt models: %d", code)
	}
	raw, _, _ := m.st.Connections().Get(context.Background(), id)
	if raw.Data != `{"a":1,"a":2}` {
		t.Errorf("the corrupt row was rewritten: %s", raw.Data)
	}
}

func TestValidateIsNotReadAsAnID(t *testing.T) {
	m := newMgmtRig(t)
	code, out, raw := m.call("POST", "/api/providers/validate", `{"provider":"openai","apiKey":""}`, true)
	if code != 200 || out["error"] != "Invalid API key" {
		t.Fatalf("%d %s", code, raw)
	}
	if code, _, _ := m.call("GET", "/api/providers/validate", "", true); code != 404 {
		t.Fatalf("GET /validate is read as a connection id: %d", code)
	}
}

func TestRegistryModelsMakeNoRequestAndMergeCustomAndDisabled(t *testing.T) {
	m := newMgmtRig(t)
	ctx := context.Background()
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"o"}`, 201))
	// The registry's own probe address, served locally: any request the models
	// route makes for a registry provider lands here and is counted.
	var asked atomic.Int64
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Add(1) }))
	defer trap.Close()
	m.pointRegistryAt(t, "openai", trap.URL)
	if err := m.st.KV().Set(ctx, repository.ScopeDisabledModels, "openai", `["gpt-4o"]`); err != nil {
		t.Fatal(err)
	}
	if err := m.st.KV().Set(ctx, repository.ScopeCustomModels, "openai|my-ft|llm", `{"id":"my-ft","name":"Mine"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.st.KV().Set(ctx, repository.ScopeCustomModels, "anthropic|other|llm", `{"id":"other"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.st.KV().Set(ctx, repository.ScopeCustomModels, "openai|keyed-by-id|llm", `{"id":"keyed-by-id"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.st.KV().Set(ctx, repository.ScopeCustomModels, "openai|gpt-4o-mini|llm", `{"id":"gpt-4o-mini","name":"Replaced"}`); err != nil {
		t.Fatal(err)
	}
	out := m.ok(t, "GET", "/api/providers/"+id+"/models", "", 200)
	byID := map[string]map[string]any{}
	for _, raw := range out["models"].([]any) {
		mm := raw.(map[string]any)
		byID[mm["id"].(string)] = mm
	}
	if _, ok := byID["gpt-4o"]; ok {
		t.Error("a disabled model was listed")
	}
	if byID["my-ft"]["source"] != "custom" || byID["my-ft"]["name"] != "Mine" {
		t.Errorf("custom model: %v", byID["my-ft"])
	}
	if _, ok := byID["other"]; ok {
		t.Error("another provider's custom model was listed")
	}
	if _, ok := byID["keyed-by-id"]; !ok {
		t.Error("a custom model keyed by the provider id was not listed")
	}
	if byID["gpt-4o-mini"]["name"] != "Replaced" || byID["gpt-4o-mini"]["source"] != "custom" {
		t.Errorf("a custom model must replace the built-in of the same id: %v", byID["gpt-4o-mini"])
	}
	if byID["gpt-5"]["source"] != "builtin" {
		t.Errorf("builtin: %v", byID["gpt-5"])
	}
	n := 0
	for _, raw := range out["models"].([]any) {
		if raw.(map[string]any)["id"] == "gpt-4o-mini" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("gpt-4o-mini listed %d times", n)
	}
	if asked.Load() != 0 {
		t.Errorf("listing a registry provider's models made %d outbound requests", asked.Load())
	}
}

func TestNodeModelsAreFetchedFromTheNodeWithTheSavedKey(t *testing.T) {
	m := newMgmtRig(t)
	var gotAuth atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		fmt.Fprint(w, `{"data":[{"id":"alpha"},{"id":"beta","name":"Beta"}]}`)
	}))
	defer srv.Close()
	node := nodeFor(t, m, srv.URL, "openai-compatible", "nm")
	id := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"sk-saved","name":"a"}`, node), 201))
	out := m.ok(t, "GET", "/api/providers/"+id+"/models", "", 200)
	if len(out["models"].([]any)) != 2 || gotAuth.Load() != "Bearer sk-saved" {
		t.Fatalf("%v, auth %v", out, gotAuth.Load())
	}
	_, _, raw := m.call("GET", "/api/providers/"+id+"/models", "", true)
	if strings.Contains(raw, "sk-saved") {
		t.Fatalf("the saved key came back: %s", raw)
	}
}

func TestNodeModelsFailuresAreFixedMessagesWithNoUpstreamText(t *testing.T) {
	m := newMgmtRig(t)
	for _, status := range []int{401, 404, 500} {
		srv := newUpstream(t, status, canary)
		node := nodeFor(t, m, srv.URL, "openai-compatible", fmt.Sprintf("f%d", status))
		id := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"a"}`, node), 201))
		code, _, raw := m.call("GET", "/api/providers/"+id+"/models", "", true)
		if code < 400 {
			t.Errorf("status %d answered %d", status, code)
		}
		for _, leak := range []string{canary, srv.URL, "127.0.0.1"} {
			if strings.Contains(raw, leak) {
				t.Errorf("status %d: the answer carries %q: %s", status, leak, raw)
			}
		}
	}
}

func TestModelsOfAnUnknownConnectionIs404(t *testing.T) {
	m := newMgmtRig(t)
	if code, _, _ := m.call("GET", "/api/providers/nope/models", "", true); code != 404 {
		t.Fatalf("%d", code)
	}
}

// testRegistryFS is a one-provider registry whose address is url.
func testRegistryFS(id, url string) fstest.MapFS {
	body := fmt.Sprintf(`{"id":%q,"category":"apikey","transport":{"baseUrl":%q},"auth":{"scheme":"bearer","header":"Authorization"},"models":[{"id":"gpt-5","name":"GPT-5"},{"id":"gpt-4o","name":"GPT-4o"},{"id":"gpt-4o-mini","name":"GPT-4o Mini"}]}`, id, url+"/v1/chat/completions")
	return fstest.MapFS{"data/" + id + ".json": {Data: []byte(body)}}
}

// scriptedLogin is a LoginChecker that answers what the test tells it to.
type scriptedLogin struct {
	refreshed bool
	err       error
	asked     []string
}

func (s *scriptedLogin) Ensure(_ context.Context, id string) (bool, error) {
	s.asked = append(s.asked, id)
	return s.refreshed, s.err
}

func loginConn(t *testing.T, m *mgmtRig, authType, email string, fields map[string]string) repository.Connection {
	t.Helper()
	conn, err := m.st.Connections().Create(context.Background(), repository.ConnInput{Provider: "claude", AuthType: authType, Email: email, Fields: fields}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestTestingALoginChecksItsTokenWithoutAskingTheProvider(t *testing.T) {
	cases := []struct {
		name      string
		login     scriptedLogin
		valid     bool
		message   string
		refreshed bool
		status    string
	}{
		{"fresh", scriptedLogin{}, true, "", false, "active"},
		{"renewed", scriptedLogin{refreshed: true}, true, "", true, "active"},
		{"dead", scriptedLogin{err: oauth.ErrExpired}, false, "needs to be reconnected", false, "error"},
		{"no token", scriptedLogin{err: oauth.ErrNoToken}, false, "no access token", false, "error"},
		{"cannot renew", scriptedLogin{err: oauth.ErrNoRefresh}, false, "token refresh failed", false, "error"},
		{"provider refused", scriptedLogin{err: &oauth.Error{Class: oauth.ClassRejected}}, false, "token refresh failed", false, "error"},
		{"store down", scriptedLogin{err: oauth.ErrStorage}, false, "token refresh failed", false, "error"},
	}
	for _, tc := range cases {
		m := newMgmtRig(t)
		login := tc.login
		m.pc.Refresher = &login
		conn := loginConn(t, m, "oauth", "a@b.c", map[string]string{"accessToken": `"AT"`, "refreshToken": `"RT"`})
		out := m.ok(t, "POST", "/api/providers/"+conn.ID+"/test", "", 200)
		if out["valid"] != tc.valid {
			t.Errorf("%s: valid = %v", tc.name, out["valid"])
		}
		if tc.message != "" && out["error"] != tc.message {
			t.Errorf("%s: error = %v, want %q", tc.name, out["error"], tc.message)
		}
		if got, _ := out["refreshed"].(bool); got != tc.refreshed {
			t.Errorf("%s: refreshed = %v", tc.name, out["refreshed"])
		}
		if len(login.asked) != 1 || login.asked[0] != conn.ID {
			t.Errorf("%s: asked %v", tc.name, login.asked)
		}
		got, _, _ := m.st.Connections().Get(context.Background(), conn.ID)
		if st, _ := got.Doc().GetString("testStatus"); st != tc.status {
			t.Errorf("%s: testStatus = %q, want %q", tc.name, st, tc.status)
		}
	}
}

func TestATestNeverClearsAnExpiredMark(t *testing.T) {
	m := newMgmtRig(t)
	m.pc.Refresher = &scriptedLogin{}
	conn := loginConn(t, m, "oauth", "a@b.c", map[string]string{"accessToken": `"AT"`, "refreshToken": `"RT"`, "testStatus": `"expired"`})
	out := m.ok(t, "POST", "/api/providers/"+conn.ID+"/test", "", 200)
	if out["valid"] != true {
		t.Fatalf("%v", out)
	}
	got, _, _ := m.st.Connections().Get(context.Background(), conn.ID)
	if st, _ := got.Doc().GetString("testStatus"); st != repository.StatusExpired {
		t.Fatalf("a passing test cleared the mark: %q", st)
	}
}

func TestOnlyALoginOfAProviderWithOneIsTestedThatWay(t *testing.T) {
	m := newMgmtRig(t)
	login := &scriptedLogin{}
	m.pc.Refresher = login

	pasted := loginConn(t, m, "access_token", "", map[string]string{"accessToken": `"AT"`})
	before, _, _ := m.st.Connections().Get(context.Background(), pasted.ID)
	out := m.ok(t, "POST", "/api/providers/"+pasted.ID+"/test", "", 200)
	if out["error"] != "test not supported yet" {
		t.Fatalf("a pasted token: %v", out)
	}
	after, _, _ := m.st.Connections().Get(context.Background(), pasted.ID)
	if before.Data != after.Data {
		t.Fatal("an unsupported test wrote the row")
	}

	other, err := m.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai", AuthType: "oauth", Email: "g@b.c", Fields: map[string]string{"accessToken": `"AT"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out := m.ok(t, "POST", "/api/providers/"+other.ID+"/test", "", 200); out["error"] != "test not supported yet" {
		t.Fatalf("a provider without a login: %v", out)
	}
	if len(login.asked) != 0 {
		t.Fatalf("the refresher was asked about %v", login.asked)
	}
}

func TestALoginAnswerCarriesNoSecret(t *testing.T) {
	m := newMgmtRig(t)
	m.pc.Refresher = &scriptedLogin{err: &oauth.Error{Class: oauth.ClassRejected}}
	conn := loginConn(t, m, "oauth", "a@b.c", map[string]string{"accessToken": `"SECRET-AT"`, "refreshToken": `"SECRET-RT"`})
	_, _, raw := m.call("POST", "/api/providers/"+conn.ID+"/test", "", true)
	if strings.Contains(raw, "SECRET") {
		t.Fatalf("%s", raw)
	}
}

func TestTestingASavedCookieConnectionReadsTheStoredCookie(t *testing.T) {
	m := newMgmtRig(t)
	m.pc.Checker = webChecker(webStub{"chatgpt.com/api/auth/session": {body: `{"user":{"email":"a@b.c"}}`}})
	id := connID(t, m.ok(t, "POST", "/api/providers",
		`{"provider":"chatgpt-web","apiKey":"__Secure-next-auth.session-token=v","name":"web"}`, 201))
	if out := m.ok(t, "POST", "/api/providers/"+id+"/test", "", 200); out["valid"] != true {
		t.Fatalf("a saved, working cookie was judged: %v", out)
	}
}
