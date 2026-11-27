package controllers

import (
	"context"
	nethttp "net/http"
	"strings"
	"testing"
	"time"

	contractshttp "github.com/goravel/framework/contracts/http"

	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"rllmx/app/store/repository"
)

func modelAdminRoutes(m *mgmtRig, c *ModelsController) {
	h := func(method, path string, fn contractshttp.HandlerFunc) {
		m.guard.Handle(m.router, apphttp.ClassProtected, method, path, fn)
	}
	h(nethttp.MethodGet, "/api/models", c.AdminList)
	h(nethttp.MethodGet, "/api/models/alias", c.Aliases)
	h(nethttp.MethodPut, "/api/models/alias", c.SetAlias)
	h(nethttp.MethodDelete, "/api/models/alias", c.DeleteAlias)
	h(nethttp.MethodGet, "/api/models/custom", c.CustomModels)
	h(nethttp.MethodPost, "/api/models/custom", c.AddCustomModel)
	h(nethttp.MethodDelete, "/api/models/custom", c.DeleteCustomModel)
	h(nethttp.MethodGet, "/api/models/disabled", c.DisabledModels)
	h(nethttp.MethodPost, "/api/models/disabled", c.DisableModels)
	h(nethttp.MethodDelete, "/api/models/disabled", c.EnableModels)
	h(nethttp.MethodGet, "/api/models/availability", c.Availability)
	h(nethttp.MethodPost, "/api/models/availability", c.ClearCooldown)
}

func newModelAdmin(t *testing.T) (*mgmtRig, *ModelsController) {
	t.Helper()
	m := newMgmtRig(t)
	caps, err := upstream.LoadCapabilities(m.pc.Registry)
	if err != nil {
		t.Fatal(err)
	}
	c := &ModelsController{
		Registry: m.pc.Registry, Caps: caps,
		DB: func() *repository.Store { return m.st.Store },
	}
	modelAdminRoutes(m, c)
	return m, c
}

func TestTheModelListCarriesCapsAndTheAliasThatPointsAtIt(t *testing.T) {
	m, _ := newModelAdmin(t)
	m.ok(t, "PUT", "/api/models/alias", `{"model":"openai/gpt-4o","alias":"fast"}`, 200)

	out := m.ok(t, "GET", "/api/models/alias", "", 200)
	if aliases := out["aliases"].(map[string]any); aliases["fast"] != "openai/gpt-4o" {
		t.Fatalf("aliases %v", aliases)
	}

	list := m.ok(t, "GET", "/api/models", "", 200)["models"].([]any)
	var found map[string]any
	for _, raw := range list {
		e := raw.(map[string]any)
		if e["fullModel"] == "openai/gpt-4o" {
			found = e
		}
	}
	if found == nil {
		t.Fatal("the aliased model is not listed")
	}
	if found["alias"] != "fast" || found["provider"] != "openai" || found["model"] != "gpt-4o" {
		t.Fatalf("%v", found)
	}
	if _, ok := found["caps"].(map[string]any)["contextWindow"]; !ok {
		t.Fatalf("no caps: %v", found)
	}
	// A member is named by the registry id a request resolves to, while the page
	// shows the alias; a combo maps its caps by both.
	for _, raw := range list {
		e := raw.(map[string]any)
		if e["provider"] == "cc" && e["providerId"] != "claude" {
			t.Fatalf("an aliased row names its id as %v", e["providerId"])
		}
	}
}

func TestTheModelListNamesACustomModelsKindForTheProviderPage(t *testing.T) {
	m, _ := newModelAdmin(t)
	m.ok(t, "POST", "/api/models/custom", `{"providerAlias":"openai","id":"my-embed","type":"embedding","name":"Mine"}`, 200)
	var found map[string]any
	for _, raw := range m.ok(t, "GET", "/api/models", "", 200)["models"].([]any) {
		if e := raw.(map[string]any); e["fullModel"] == "openai/my-embed" {
			found = e
		}
	}
	if found == nil {
		t.Fatal("the custom model is not listed")
	}
	if found["kind"] != "embedding" || found["model"] != "my-embed" || found["source"] != "custom" {
		t.Fatalf("%v", found)
	}
}

func TestAnAliasCannotTakeANameRoutingAlreadyUsesOrAnotherModels(t *testing.T) {
	m, _ := newModelAdmin(t)
	m.ok(t, "PUT", "/api/models/alias", `{"model":"openai/gpt-4o","alias":"fast"}`, 200)
	for name, body := range map[string]string{
		"another model": `{"model":"openai/gpt-4o-mini","alias":"fast"}`,
	} {
		if code, _, raw := m.call("PUT", "/api/models/alias", body, true); code != nethttp.StatusConflict {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, raw := m.call("PUT", "/api/models/alias", `{"model":"openai/gpt-4o","alias":"fast"}`, true); code != 200 {
		t.Errorf("the same alias was refused: %d %s", code, raw)
	}
	for name, body := range map[string]string{
		"provider name": `{"model":"openai/gpt-4o","alias":"openai"}`,
		"slash":         `{"model":"openai/gpt-4o","alias":"a/b"}`,
		"empty model":   `{"model":"","alias":"x"}`,
		"not a target":  `{"model":"gpt-4o","alias":"x"}`,
	} {
		if code, _, raw := m.call("PUT", "/api/models/alias", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if code, _, _ := m.call("DELETE", "/api/models/alias?alias=", "", true); code != 400 {
		t.Errorf("an empty delete: %d", code)
	}
	if code, _, _ := m.call("DELETE", "/api/models/alias?alias=fast", "", true); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _, _ := m.call("DELETE", "/api/models/alias?alias=fast", "", true); code != 200 {
		t.Errorf("deleting a missing alias must succeed: %d", code)
	}
}

func TestACustomModelIsStoredInTheShapeTheCatalogReads(t *testing.T) {
	m, _ := newModelAdmin(t)
	ctx := context.Background()
	out := m.ok(t, "POST", "/api/models/custom",
		`{"providerAlias":"cc","id":"claude-x","type":"llm","name":"Claude X","caps":{"vision":true,"audio":true,"tools":true}}`, 200)
	if out["added"] != true {
		t.Fatalf("%v", out)
	}
	raw, ok, err := m.st.Store.KV().Get(ctx, repository.ScopeCustomModels, "cc|claude-x|llm")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	for _, want := range []string{`"providerAlias":"cc"`, `"id":"claude-x"`, `"type":"llm"`, `"name":"Claude X"`, `"vision":true`, `"audio":true`} {
		if !strings.Contains(raw, want) {
			t.Errorf("stored %s lacks %s", raw, want)
		}
	}
	if strings.Contains(raw, "tools") {
		t.Errorf("a capability the reference does not keep was stored: %s", raw)
	}
	if out := m.ok(t, "POST", "/api/models/custom", `{"providerAlias":"cc","id":"claude-x","type":"llm"}`, 200); out["added"] != false {
		t.Fatalf("a second write must be an update: %v", out)
	}
	for name, body := range map[string]string{
		"unknown provider":  `{"providerAlias":"nope","id":"x"}`,
		"unknown type":      `{"providerAlias":"cc","id":"x","type":"hologram"}`,
		"control character": `{"providerAlias":"cc","id":"x\u0000"}`,
		"no id":             `{"providerAlias":"cc"}`,
	} {
		if code, _, raw := m.call("POST", "/api/models/custom", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if out := m.ok(t, "POST", "/api/models/custom", `{"providerAlias":"cc","id":"tts-1","type":"tts","transport":"gemini-live"}`, 200); out["added"] != true {
		t.Fatalf("%v", out)
	}
	raw, _, _ = m.st.Store.KV().Get(ctx, repository.ScopeCustomModels, "cc|tts-1|tts")
	if strings.Contains(raw, "transport") {
		t.Errorf("a transport outside stt was kept: %s", raw)
	}
	list := m.ok(t, "GET", "/api/models/custom", "", 200)["models"].([]any)
	if len(list) != 2 {
		t.Fatalf("models %v", list)
	}
	m.ok(t, "DELETE", "/api/models/custom?providerAlias=cc&id=claude-x&type=llm", "", 200)
	if _, ok, _ := m.st.Store.KV().Get(ctx, repository.ScopeCustomModels, "cc|claude-x|llm"); ok {
		t.Fatal("the row was not deleted")
	}
}

func TestDisabledModelsAreAUnionAndADeleteEnables(t *testing.T) {
	m, _ := newModelAdmin(t)
	m.ok(t, "POST", "/api/models/disabled", `{"providerAlias":"openai","ids":["a","b"]}`, 200)
	m.ok(t, "POST", "/api/models/disabled", `{"providerAlias":"openai","ids":["b","c"]}`, 200)
	ids := m.ok(t, "GET", "/api/models/disabled?providerAlias=openai", "", 200)["ids"].([]any)
	if len(ids) != 3 {
		t.Fatalf("ids %v", ids)
	}
	all := m.ok(t, "GET", "/api/models/disabled", "", 200)["disabled"].(map[string]any)
	if len(all["openai"].([]any)) != 3 {
		t.Fatalf("the whole list %v", all)
	}
	m.ok(t, "DELETE", "/api/models/disabled?providerAlias=openai&id=b", "", 200)
	ids = m.ok(t, "GET", "/api/models/disabled?providerAlias=openai", "", 200)["ids"].([]any)
	if len(ids) != 2 {
		t.Fatalf("after one removal %v", ids)
	}
	m.ok(t, "DELETE", "/api/models/disabled?providerAlias=openai&id=a", "", 200)
	m.ok(t, "DELETE", "/api/models/disabled?providerAlias=openai&id=c", "", 200)
	if ids := m.ok(t, "GET", "/api/models/disabled?providerAlias=openai", "", 200)["ids"].([]any); len(ids) != 0 {
		t.Fatalf("an empty list must drop the row: %v", ids)
	}
	if code, _, _ := m.call("POST", "/api/models/disabled", `{"providerAlias":"openai"}`, true); code != 400 {
		t.Errorf("no ids[]: %d", code)
	}
	if code, _, _ := m.call("DELETE", "/api/models/disabled", "", true); code != 400 {
		t.Errorf("no providerAlias: %d", code)
	}
}

func TestAvailabilityListsTheLocksThatStillHoldAndClearsTheAccountOne(t *testing.T) {
	m, _ := newModelAdmin(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201))
	ctx := context.Background()
	locks := map[string]string{
		repository.LockKey("gpt-4o"): document.Encode(dialect.FormatTimestamp(time.Now().Add(time.Hour))),
		repository.LockKey("__all"):  document.Encode(dialect.FormatTimestamp(time.Now().Add(time.Hour))),
		repository.LockKey("gone"):   document.Encode(dialect.FormatTimestamp(time.Now().Add(-time.Hour))),
	}
	if _, err := m.st.Store.Connections().Update(ctx, id, repository.ConnPatch{Fields: locks}); err != nil {
		t.Fatal(err)
	}

	out := m.ok(t, "GET", "/api/models/availability", "", 200)
	models := out["models"].([]any)
	seen := map[string]bool{}
	for _, raw := range models {
		e := raw.(map[string]any)
		seen[e["model"].(string)] = true
		if e["status"] != "cooldown" || e["connectionName"] != "a" {
			t.Fatalf("%v", e)
		}
	}
	if !seen["gpt-4o"] || !seen["__all"] || seen["gone"] {
		t.Fatalf("seen %v", seen)
	}

	if code, _, raw := m.call("POST", "/api/models/availability", `{"action":"nope","provider":"openai","model":"__all"}`, true); code != 400 {
		t.Fatalf("%d %s", code, raw)
	}
	m.ok(t, "POST", "/api/models/availability", `{"action":"clearCooldown","provider":"openai","model":"gpt-4o"}`, 200)
	out = m.ok(t, "GET", "/api/models/availability", "", 200)
	for _, raw := range out["models"].([]any) {
		if raw.(map[string]any)["model"] == "gpt-4o" {
			t.Fatal("the model lock survived the clear")
		}
	}

	account := map[string]string{repository.LockKey("__all"): document.Encode(dialect.FormatTimestamp(time.Now().Add(time.Hour)))}
	if _, err := m.st.Store.Connections().Update(ctx, id, repository.ConnPatch{Fields: account}); err != nil {
		t.Fatal(err)
	}
	m.ok(t, "POST", "/api/models/availability", `{"action":"clearCooldown","provider":"openai","model":"__all"}`, 200)
	out = m.ok(t, "GET", "/api/models/availability", "", 200)
	for _, raw := range out["models"].([]any) {
		if raw.(map[string]any)["model"] == "__all" {
			t.Fatal("the account lock survived the clear")
		}
	}
}

func TestUnavailableAccountWithoutALockIsListedOnce(t *testing.T) {
	m, _ := newModelAdmin(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201))
	if _, err := m.st.Store.Connections().Update(context.Background(), id,
		repository.ConnPatch{Fields: map[string]string{"testStatus": `"unavailable"`, "lastError": `"boom"`}}); err != nil {
		t.Fatal(err)
	}
	models := m.ok(t, "GET", "/api/models/availability", "", 200)["models"].([]any)
	if len(models) != 1 {
		t.Fatalf("models %v", models)
	}
	e := models[0].(map[string]any)
	if e["status"] != "unavailable" || e["model"] != "__all" || e["lastError"] != "boom" {
		t.Fatalf("%v", e)
	}
}

// The page stores the provider's alias; a request resolves the registry id, and
// an unticked box on the edit form must turn the flag off rather than be lost.
func TestADeclaredFlagReachesTheLookupUnderTheAliasThePageStores(t *testing.T) {
	m, c := newModelAdmin(t)
	m.ok(t, "POST", "/api/models/custom", `{"providerAlias":"cc","id":"claude-z","type":"llm","caps":{"vision":true}}`, 200)
	if !c.Caps.For("claude", "claude-z").Vision {
		t.Fatal("the declared vision did not reach the id a request names")
	}
	found := false
	for _, raw := range m.ok(t, "GET", "/api/models", "", 200)["models"].([]any) {
		row := raw.(map[string]any)
		if row["model"] == "claude-z" {
			found = true
			if row["caps"].(map[string]any)["vision"] != true {
				t.Fatalf("the page shows %v", row["caps"])
			}
		}
	}
	if !found {
		t.Fatal("the custom model is not listed")
	}
	m.ok(t, "POST", "/api/models/custom", `{"providerAlias":"cc","id":"claude-z","type":"llm","caps":{"vision":false}}`, 200)
	if c.Caps.For("claude", "claude-z").Vision {
		t.Fatal("a declared off was ignored")
	}
}
