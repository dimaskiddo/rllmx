package controllers

import (
	"context"
	nethttp "net/http"
	"testing"

	"github.com/tidwall/gjson"

	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

type comboRig struct {
	m       *mgmtRig
	c       *CombosController
	resets  []string
	changed int
}

func newComboRig(t *testing.T) *comboRig {
	t.Helper()
	m := newMgmtRig(t)
	r := &comboRig{m: m}
	r.c = &CombosController{
		DB: func() *repository.Store { return m.st.Store }, Registry: m.pc.Registry,
		Reset:   func(names ...string) { r.resets = append(r.resets, names...) },
		Changed: func() { r.changed++ },
	}
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos", r.c.List)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos", r.c.Create)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/presets", r.c.Presets)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos/presets", r.c.ApplyPresets)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/{id}", r.c.Show)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodPut, "/api/combos/{id}", r.c.Update)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/combos/{id}", r.c.Delete)
	return r
}

func (r *comboRig) id(t *testing.T, out map[string]any) string {
	t.Helper()
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("no combo id in %v", out)
	}
	return id
}

func TestAComboCanBeCreatedEmptyAndFilledLater(t *testing.T) {
	r := newComboRig(t)
	created := r.m.ok(t, "POST", "/api/combos", `{"name":"media","kind":"image"}`, 201)
	id := r.id(t, created)
	if created["kind"] != "image" || len(created["models"].([]any)) != 0 {
		t.Fatalf("%v", created)
	}
	if code, _, raw := r.m.call("POST", "/api/combos", `{"name":"media"}`, true); code != nethttp.StatusConflict {
		t.Fatalf("a duplicate name: %d %s", code, raw)
	}
	r.m.ok(t, "PUT", "/api/combos/"+id, `{"models":["openai/gpt-4o"]}`, 200)
	got := r.m.ok(t, "GET", "/api/combos/"+id, "", 200)
	if len(got["models"].([]any)) != 1 || got["name"] != "media" {
		t.Fatalf("%v", got)
	}
	list := r.m.ok(t, "GET", "/api/combos", "", 200)["combos"].([]any)
	if len(list) != 1 {
		t.Fatalf("list %v", list)
	}
	r.m.ok(t, "DELETE", "/api/combos/"+id, "", 200)
	if code, _, raw := r.m.call("GET", "/api/combos/"+id, "", true); code != nethttp.StatusNotFound {
		t.Fatalf("after delete: %d %s", code, raw)
	}
}

func TestARenameCarriesTheStrategyAndADeleteDropsIt(t *testing.T) {
	r := newComboRig(t)
	ctx := context.Background()
	id := r.id(t, r.m.ok(t, "POST", "/api/combos", `{"name":"fast","models":["openai/gpt-4o"]}`, 201))
	if err := r.m.st.Store.Settings().MergeObjectField(ctx, "comboStrategies", "fast", `{"fallbackStrategy":"smart"}`); err != nil {
		t.Fatal(err)
	}

	renamed := r.m.ok(t, "PUT", "/api/combos/"+id, `{"name":"faster"}`, 200)
	if renamed["name"] != "faster" || len(renamed["models"].([]any)) != 1 {
		t.Fatalf("%v", renamed)
	}
	d, err := r.m.st.Store.Settings().Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := d.GetRaw("comboStrategies")
	if gjson.Get(raw, "fast").Exists() || gjson.Get(raw, "faster.fallbackStrategy").String() != "smart" {
		t.Fatalf("the strategy did not move: %s", raw)
	}
	if r.changed == 0 || len(r.resets) < 2 {
		t.Fatalf("the hooks did not run: changed=%d resets=%v", r.changed, r.resets)
	}

	r.m.ok(t, "DELETE", "/api/combos/"+id, "", 200)
	d, _ = r.m.st.Store.Settings().Get(ctx)
	raw, _ = d.GetRaw("comboStrategies")
	if gjson.Get(raw, "faster").Exists() {
		t.Fatalf("the strategy was left behind: %s", raw)
	}
}

func TestARenameToATakenNameIsAConflictAndTheComboStays(t *testing.T) {
	r := newComboRig(t)
	id := r.id(t, r.m.ok(t, "POST", "/api/combos", `{"name":"one","models":["a/b"]}`, 201))
	r.m.ok(t, "POST", "/api/combos", `{"name":"two","models":["a/c"]}`, 201)
	if code, _, raw := r.m.call("PUT", "/api/combos/"+id, `{"name":"two"}`, true); code != nethttp.StatusConflict {
		t.Fatalf("%d %s", code, raw)
	}
	if got := r.m.ok(t, "GET", "/api/combos/"+id, "", 200); got["name"] != "one" {
		t.Fatalf("%v", got)
	}
	for name, body := range map[string]string{
		"slash": `{"name":"a/b"}`,
		"kind":  `{"kind":"a\u0000b"}`,
		"model": `{"models":["a\u0000b"]}`,
	} {
		if code, _, raw := r.m.call("PUT", "/api/combos/"+id, body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

func TestPresetsListTheProvidersModelsAndSkipAnExistingName(t *testing.T) {
	r := newComboRig(t)
	ctx := context.Background()
	entry, ok := r.c.Registry.ByID("claude")
	if !ok || len(entry.Models) == 0 {
		t.Fatal("no claude entry")
	}
	// An alias that carries a model's own id makes two raw presets share a name,
	// the case the list must fold to one.
	if err := r.m.st.Store.KV().Set(ctx, repository.ScopeModelAliases, entry.Models[0].ID, `"cc/claude-sonnet-5"`); err != nil {
		t.Fatal(err)
	}

	r.m.ok(t, "POST", "/api/combos", `{"name":"`+entry.Models[0].ID+`"}`, 201)
	out := r.m.ok(t, "GET", "/api/combos/presets?source=claude", "", 200)
	items := out["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no claude preset")
	}
	seen := map[string]bool{}
	exists := 0
	for _, raw := range items {
		it := raw.(map[string]any)
		name := it["name"].(string)
		if seen[name] {
			t.Fatalf("the list repeats %q: %v", name, items)
		}
		seen[name] = true
		if it["exists"] == true {
			exists++
		}
		if models := it["models"].([]any); len(models) == 0 {
			t.Fatalf("%v", it)
		}
	}
	if exists != 1 {
		t.Fatalf("the existing name was not marked: %v", items)
	}
	if out["toCreate"] != float64(len(items)-1) || out["toSkip"] != float64(1) {
		t.Fatalf("%v", out)
	}

	applied := r.m.ok(t, "POST", "/api/combos/presets", `{"source":"claude"}`, 200)
	if applied["createdCount"] != float64(len(items)-1) || applied["skippedCount"] != float64(1) {
		t.Fatalf("%v", applied)
	}
	cursor := r.m.ok(t, "GET", "/api/combos/presets?source=cursor", "", 200)
	if len(cursor["items"].([]any)) == 0 {
		t.Fatal("no cursor preset")
	}
	if code, _, _ := r.m.call("GET", "/api/combos/presets?source=nope", "", true); code != 400 {
		t.Fatalf("an unknown source: %d", code)
	}
}
