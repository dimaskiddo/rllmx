package repository

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/store/document"
	"rllmx/tests"
)

func TestSettingsStartEmptyAndNeverPersistDefaults(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	doc, err := s.Settings().Get(ctx)
	if err != nil || doc.String() != "{}" {
		t.Fatalf("empty settings = %q %v", doc.String(), err)
	}
	eff, err := s.Settings().Effective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := eff.GetRaw("requireLogin"); v != "true" {
		t.Fatalf("default requireLogin = %q", v)
	}
	if v, _ := eff.GetRaw("stickyRoundRobinLimit"); v != "3" {
		t.Fatalf("default stickyRoundRobinLimit = %q", v)
	}
	if v, _ := eff.GetRaw("samlIssuer"); v != `""` {
		t.Fatalf("samlIssuer defaults to empty so the entity id derives from APP_URL, got %q", v)
	}
	again, _ := s.Settings().Get(ctx)
	if again.String() != "{}" {
		t.Fatalf("reading effective settings persisted defaults: %s", again.String())
	}
}

func TestSettingsPatchIsAShallowMergeThatKeepsUnknownKeysAndNull(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().Patch(ctx, map[string]string{"alpha": `{"x":1,"y":2}`, "taste-skillEnabled": `true`, "zzz-unknown": `"keep me"`}); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().Patch(ctx, map[string]string{"alpha": `{"x":9}`, "nulled": `null`}); err != nil {
		t.Fatal(err)
	}
	doc, _ := s.Settings().Get(ctx)
	if v, _ := doc.GetRaw("alpha"); v != `{"x":9}` {
		t.Fatalf("a nested object must be replaced whole, got %s", v)
	}
	if v, _ := doc.GetRaw("zzz-unknown"); v != `"keep me"` {
		t.Fatalf("unknown key lost: %s", doc.String())
	}
	if v, _ := doc.GetRaw("taste-skillEnabled"); v != "true" {
		t.Fatalf("hyphenated key lost: %s", doc.String())
	}
	if !doc.IsNull("nulled") {
		t.Fatalf("null was dropped: %s", doc.String())
	}
}

func TestSettingsPatchRejectsProtectedKeys(t *testing.T) {
	s := newMigrated(t)
	for _, k := range []string{"password", "mitmSudoEncrypted"} {
		if err := s.Settings().Patch(context.Background(), map[string]string{k: `"x"`}); !errors.Is(err, ErrProtectedSetting) {
			t.Errorf("Patch(%s) err = %v, want ErrProtectedSetting", k, err)
		}
	}
	if err := s.Settings().SetPasswordHash(context.Background(), "$2a$10$abc"); err != nil {
		t.Fatal(err)
	}
	doc, _ := s.Settings().Get(context.Background())
	if v, _ := doc.GetString("password"); v != "$2a$10$abc" {
		t.Fatalf("password not stored: %s", doc.String())
	}
}

func TestSettingsPatchRejectsInvalidJSONValue(t *testing.T) {
	s := newMigrated(t)
	if err := s.Settings().Patch(context.Background(), map[string]string{"a": `{not json`}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if err := s.Settings().Patch(context.Background(), map[string]string{"": `1`}); err == nil {
		t.Fatal("an empty key was accepted")
	}
}

func TestSettingsConcurrentPatchesLoseNothing(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key := "k" + string(rune('a'+i))
			if err := s.Settings().Patch(ctx, map[string]string{key: `1`}); err != nil {
				t.Errorf("patch: %v", err)
			}
		}()
	}
	wg.Wait()
	doc, _ := s.Settings().Get(ctx)
	if n := len(doc.Keys()); n != 20 {
		t.Fatalf("%d keys survived 20 concurrent patches: %s", n, doc.String())
	}
}

func TestSettingsEffectiveRewritesOnReadOnly(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().Patch(ctx, map[string]string{
		"capacityAdapter": `{"vision":{"enabled":true,"models":["oc/mimo-v2.5-free","x/y"]}}`,
	}); err != nil {
		t.Fatal(err)
	}
	eff, _ := s.Settings().Effective(ctx)
	if v, _ := eff.GetRaw("capacityAdapter"); !strings.Contains(v, "oc/mimo-v2.6-flash-free") || strings.Contains(v, "v2.5") {
		t.Fatalf("mimo model not renamed on read: %s", v)
	}
	stored, _ := s.Settings().Get(ctx)
	if v, _ := stored.GetRaw("capacityAdapter"); !strings.Contains(v, "v2.5") {
		t.Fatalf("the rename was persisted: %s", v)
	}
}

func TestSettingsDefaultsKeepAStoredValue(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().Patch(ctx, map[string]string{"requireLogin": `false`, "stickyRoundRobinLimit": `7`}); err != nil {
		t.Fatal(err)
	}
	eff, _ := s.Settings().Effective(ctx)
	if v, _ := eff.GetRaw("requireLogin"); v != "false" {
		t.Fatalf("a stored false was replaced by the default: %q", v)
	}
	if v, _ := eff.GetRaw("stickyRoundRobinLimit"); v != "7" {
		t.Fatalf("stickyRoundRobinLimit = %q", v)
	}
}

func TestSettingsDefaultsAreValidJSONAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range settingDefaults {
		if seen[d.key] {
			t.Errorf("default %s listed twice", d.key)
		}
		seen[d.key] = true
		if _, err := document.ParseDoc(`{"v":` + d.raw + `}`); err != nil {
			t.Errorf("default %s is not valid JSON: %s", d.key, d.raw)
		}
	}
	for _, want := range []string{"responseCache", "samlIdpEntityId", "customPromptEnabled", "headroomAutoStart"} {
		if !seen[want] {
			t.Errorf("default for %s missing", want)
		}
	}
}

func TestKVBasics(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	kv := s.KV()
	if err := kv.Set(ctx, "modelAliases", "fast", `"cx/gpt-5"`); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := kv.Get(ctx, "modelAliases", "fast"); !ok || v != `"cx/gpt-5"` {
		t.Fatalf("got %q %v", v, ok)
	}
	if err := kv.Set(ctx, "modelAliases", "fast", `"cx/gpt-6"`); err != nil {
		t.Fatal(err)
	}
	all, _ := kv.List(ctx, "modelAliases")
	if len(all) != 1 || all["fast"] != `"cx/gpt-6"` {
		t.Fatalf("upsert failed: %v", all)
	}
	if _, ok, _ := kv.Get(ctx, "other", "fast"); ok {
		t.Fatal("scopes leak into each other")
	}
	if err := kv.Set(ctx, "s", "k", `{bad`); err == nil {
		t.Fatal("invalid JSON value accepted")
	}
}

func TestKVResponseCacheEnabledIsOnlyAJSONTrue(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	kv := s.KV()
	for key, want := range map[string]bool{
		"on": true, "off": false, "string": false, "number": false, "array": false, "scalar": false, "empty": false,
	} {
		raw := map[string]string{
			"on": `{"enabled":true}`, "off": `{"enabled":false}`, "string": `{"enabled":"true"}`, "number": `{"enabled":1}`,
			"array": `[true]`, "scalar": `true`, "empty": `{}`,
		}[key]
		if err := kv.Set(ctx, ScopeResponseCache, key, raw); err != nil {
			t.Fatal(err)
		}
		if got, err := kv.ResponseCacheEnabled(ctx, key); err != nil || got != want {
			t.Errorf("%s %s: %v %v, want %v", key, raw, got, err, want)
		}
	}
	if got, err := kv.ResponseCacheEnabled(ctx, "absent"); err != nil || got {
		t.Fatalf("a missing row: %v %v", got, err)
	}
	if err := kv.Set(ctx, "otherScope", "on", `{"enabled":true}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := kv.ResponseCacheEnabled(ctx, "on2"); got {
		t.Fatal("another key's row was read")
	}
}

func TestKVDisabledModels(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if got, err := s.KV().DisabledModels(ctx, "openai"); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty = %#v %v", got, err)
	}
	if err := s.KV().Set(ctx, ScopeDisabledModels, "openai", `["gpt-5",7,"gpt-6"]`); err != nil {
		t.Fatal(err)
	}
	got, _ := s.KV().DisabledModels(ctx, "openai")
	if !reflect.DeepEqual(got, []string{"gpt-5", "gpt-6"}) {
		t.Fatalf("got %v", got)
	}
}

func TestKVIncrementLosesNoUpdate(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	const n = 40
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.KV().Increment(ctx, "counters", "c"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if v, ok, _ := s.KV().Get(ctx, "counters", "c"); !ok || v != strconv.Itoa(n) {
		t.Fatalf("counter = %q, want %d", v, n)
	}
}

func TestDeleteRemovesOneKVValueAndToleratesAMissingOne(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.KV().Set(ctx, ScopeKeyLimits, "k1", `{"requests":{"day":10}}`); err != nil {
		t.Fatal(err)
	}
	if err := s.KV().Set(ctx, ScopeKeyLimits, "k2", `{}`); err != nil {
		t.Fatal(err)
	}
	if err := s.KV().Delete(ctx, ScopeKeyLimits, "k1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.KV().Get(ctx, ScopeKeyLimits, "k1"); ok {
		t.Fatal("the deleted value is still there")
	}
	if v, ok, _ := s.KV().Get(ctx, ScopeKeyLimits, "k2"); !ok || v != "{}" {
		t.Fatalf("another key's value went with it: %q %v", v, ok)
	}
	if err := s.KV().Delete(ctx, ScopeKeyLimits, "never-set"); err != nil {
		t.Fatalf("deleting a missing value: %v", err)
	}
}

func TestNodesCRUDAndCascade(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	n, err := s.Nodes().CreateUnique(ctx, Node{ID: "openai-compatible-chat-abc", Type: "openai-compatible", Name: "Local", Data: `{"prefix":"lo","apiType":"chat","baseUrl":"http://x/v1"}`}, "lo")
	if err != nil {
		t.Fatal(err)
	}
	if n.CreatedAt == "" || n.UpdatedAt == "" {
		t.Fatalf("timestamps missing: %+v", n)
	}
	putCustomModel(t, s, "openai-compatible-chat-abc", "m1")
	putCustomModel(t, s, "openai-compatible-chat-abcd", "m2")
	putCustomModel(t, s, "other", "m3")

	got, ok, _ := s.Nodes().Get(ctx, "openai-compatible-chat-abc")
	if !ok || got.Name != "Local" {
		t.Fatalf("get: %+v %v", got, ok)
	}
	upd, err := s.Nodes().Update(ctx, "openai-compatible-chat-abc", map[string]string{"baseUrl": `"http://y/v1"`})
	if err != nil || !strings.Contains(upd.Data, "http://y/v1") || !strings.Contains(upd.Data, `"prefix":"lo"`) {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if _, err := s.Nodes().DeleteCascade(ctx, "openai-compatible-chat-abc"); err != nil {
		t.Fatal(err)
	}
	left, _ := s.KV().List(ctx, "customModels")
	if _, ok := left["openai-compatible-chat-abc|m1|llm"]; ok {
		t.Fatal("the node's custom model survived the delete")
	}
	if len(left) != 2 {
		t.Fatalf("delete removed models of other nodes (id prefix collision): %v", left)
	}
	if list, _ := s.Nodes().List(ctx, ""); len(list) != 0 {
		t.Fatalf("node still listed: %+v", list)
	}
}

func TestNodeListFiltersByType(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	mkNode(t, s, "a", "openai-compatible", `{}`)
	mkNode(t, s, "b", "custom-embedding", `{}`)
	if list, _ := s.Nodes().List(ctx, "custom-embedding"); len(list) != 1 || list[0].ID != "b" {
		t.Fatalf("list = %+v", list)
	}
	if list, _ := s.Nodes().List(ctx, ""); len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
}

func TestNodeUpdateSetsTheColumnsForNameAndType(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	mkNode(t, s, "a", "t1", `{"x":1}`)
	n, err := s.Nodes().Update(ctx, "a", map[string]string{"name": `"Renamed"`, "type": `"t2"`, "y": `2`})
	if err != nil || n.Name != "Renamed" || n.Type != "t2" {
		t.Fatalf("update: %+v %v", n, err)
	}
	if strings.Contains(n.Data, `"name"`) || strings.Contains(n.Data, `"type"`) || !strings.Contains(n.Data, `"y":2`) {
		t.Fatalf("name and type belong in their columns, not in data: %s", n.Data)
	}
	if _, err := s.Nodes().Update(ctx, "a", map[string]string{"k": `{bad`}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	if _, err := s.Nodes().Update(ctx, "nope", map[string]string{"k": `1`}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing node: %v", err)
	}
}

func TestNodeCreateIsAnUpsert(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if _, err := s.Nodes().CreateUnique(ctx, Node{ID: "n1", Type: "t", Name: "one", Data: `{"a":1}`}, ""); err != nil {
		t.Fatal(err)
	}
	first, _, _ := s.Nodes().Get(ctx, "n1")
	if _, err := s.Nodes().CreateUnique(ctx, Node{ID: "n1", Type: "t", Name: "two", Data: `{"a":2}`}, ""); err != nil {
		t.Fatal(err)
	}
	second, _, _ := s.Nodes().Get(ctx, "n1")
	if second.Name != "two" || second.CreatedAt != first.CreatedAt {
		t.Fatalf("upsert must replace the row and keep createdAt: %+v vs %+v", first, second)
	}
	if _, err := s.Nodes().CreateUnique(ctx, Node{ID: "n2", Type: "t", Name: "x", Data: `{broken`}, ""); err == nil {
		t.Fatal("node data that is not JSON was stored")
	}
}

func TestNodeCreateGivesAnIDWhenNoneIsSent(t *testing.T) {
	s := newMigrated(t)
	n, err := s.Nodes().CreateUnique(context.Background(), Node{Type: "t", Name: "x", Data: `{}`}, "")
	if err != nil || len(n.ID) != 36 {
		t.Fatalf("%+v %v", n, err)
	}
}

func TestCombosCRUD(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	c, err := s.Combos().Create(ctx, "best", "", []string{"cx/gpt-5", "anthropic/claude"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != "" || c.ID == "" {
		t.Fatalf("combo: %+v", c)
	}
	if _, err := s.Combos().Create(ctx, "best", "auto", nil); !errors.Is(err, ErrComboNameTaken) {
		t.Fatalf("duplicate combo name: %v", err)
	}
	auto, err := s.Combos().Create(ctx, "auto-made", "auto", []string{"openai/gpt-4o"})
	if err != nil || auto.Kind != "auto" {
		t.Fatalf("kind not stored: %+v %v", auto, err)
	}
	got, ok, _ := s.Combos().GetByID(ctx, c.ID)
	if !ok || !reflect.DeepEqual(got.Models, []string{"cx/gpt-5", "anthropic/claude"}) {
		t.Fatalf("by id: %+v", got)
	}
	if n := s.one(t, `SELECT count(*) FROM "combos" WHERE "id"=? AND "kind" IS NULL`, c.ID); n != "1" {
		t.Fatal("kind must stay NULL")
	}
	upd, err := s.Combos().Update(ctx, c.ID, "renamed", []string{"x/y"})
	if err != nil || upd.Name != "renamed" || upd.UpdatedAt == "" {
		t.Fatalf("update: %+v %v", upd, err)
	}
	if _, err := s.Combos().Update(ctx, "nope", "n", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing combo: %v", err)
	}
	if ok, _ := s.Combos().Delete(ctx, c.ID); !ok {
		t.Fatal("delete reported nothing deleted")
	}
	if ok, _ := s.Combos().Delete(ctx, c.ID); ok {
		t.Fatal("deleting twice reported success")
	}
}

func TestCombosListedByCreationTime(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	for _, n := range []string{"c", "a", "b"} {
		if _, err := s.Combos().Create(ctx, n, "", []string{"x"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	list, _ := s.Combos().List(ctx)
	var names []string
	for _, c := range list {
		names = append(names, c.Name)
	}
	if strings.Join(names, ",") != "c,a,b" {
		t.Fatalf("order = %v", names)
	}
}

func TestAPIKeysCRUDAndAccess(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	k, err := s.APIKeys().Create(ctx, "ci", "machine0123456789", "sk-machine0123456789-abc123-deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if !k.IsActive || k.AccessRestricted || len(k.AccessAllow) != 0 {
		t.Fatalf("new key = %+v", k)
	}
	got, ok, _ := s.APIKeys().GetByKey(ctx, "sk-machine0123456789-abc123-deadbeef")
	if !ok || got.ID != k.ID {
		t.Fatalf("by key: %+v %v", got, ok)
	}
	if raw := s.one(t, `SELECT "accessAllow" FROM "apiKeys" WHERE "id"=?`, k.ID); raw != "[]" {
		t.Fatalf("unrestricted accessAllow must be '[]', got %q", raw)
	}
	upd, err := s.APIKeys().Update(ctx, k.ID, APIKeyPatch{Access: &KeyAccess{Restricted: true, Allow: []string{" cx/gpt-5 ", "CX/GPT-5", "", "combo-a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !upd.AccessRestricted || !reflect.DeepEqual(upd.AccessAllow, []string{"cx/gpt-5", "combo-a"}) {
		t.Fatalf("allow list must be trimmed, de-duplicated case-insensitively and cleaned: %+v", upd)
	}
	off := false
	upd, _ = s.APIKeys().Update(ctx, k.ID, APIKeyPatch{IsActive: &off})
	if upd.IsActive || !upd.AccessRestricted {
		t.Fatalf("toggle must not touch access: %+v", upd)
	}
	back, _, _ := s.APIKeys().GetByID(ctx, k.ID)
	if back.IsActive || !reflect.DeepEqual(back.AccessAllow, []string{"cx/gpt-5", "combo-a"}) {
		t.Fatalf("stored key = %+v", back)
	}
	if _, err := s.APIKeys().Create(ctx, "dup", "m", "sk-machine0123456789-abc123-deadbeef"); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if _, err := s.APIKeys().Update(ctx, "nope", APIKeyPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
	unrestrict := KeyAccess{}
	upd, _ = s.APIKeys().Update(ctx, k.ID, APIKeyPatch{Access: &unrestrict})
	if upd.AccessRestricted || len(upd.AccessAllow) != 0 {
		t.Fatalf("lifting the restriction must clear the list: %+v", upd)
	}
	if ok, _ := s.APIKeys().Delete(ctx, k.ID); !ok {
		t.Fatal("delete failed")
	}
	if ok, _ := s.APIKeys().Delete(ctx, k.ID); ok {
		t.Fatal("deleting twice reported success")
	}
}

func TestAPIKeysListOldestFirst(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	for _, n := range []string{"a", "b", "c"} {
		if _, err := s.APIKeys().Create(ctx, n, "m", "sk-m-"+n+"-bbbbbbbb"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	list, err := s.APIKeys().List(ctx)
	if err != nil || len(list) != 3 || list[0].Name != "a" || list[2].Name != "c" {
		t.Fatalf("list = %+v %v", list, err)
	}
}

func TestCorruptAccessAllowFailsClosed(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	k, _ := s.APIKeys().Create(ctx, "x", "m", "sk-m-aaaaaa-bbbbbbbb")
	s.exec(t, `UPDATE "apiKeys" SET "accessRestricted"=1, "accessAllow"='{broken' WHERE "id"=?`, k.ID)
	got, _, _ := s.APIKeys().GetByID(ctx, k.ID)
	if !got.AccessRestricted || len(got.AccessAllow) != 0 {
		t.Fatalf("a restricted key with a corrupt allow list must allow nothing: %+v", got)
	}
	name := "renamed"
	if _, err := s.APIKeys().Update(ctx, k.ID, APIKeyPatch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if raw := s.one(t, `SELECT "accessAllow" FROM "apiKeys" WHERE "id"=?`, k.ID); raw != "{broken" {
		t.Fatalf("a rename erased the access list that failed to parse: %q", raw)
	}
	s.exec(t, `UPDATE "apiKeys" SET "accessRestricted"=0, "accessAllow"='{broken' WHERE "id"=?`, k.ID)
	got, _, _ = s.APIKeys().GetByID(ctx, k.ID)
	if got.AccessRestricted {
		t.Fatalf("an unrestricted key must stay unrestricted: %+v", got)
	}
}

func TestAPIKeyHasNoUpdatedAtColumn(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	if n := s.one(t, `SELECT count(*) FROM pragma_table_info('apiKeys') WHERE name='updatedAt'`); n != "0" {
		t.Fatal("apiKeys must keep the reference's columns")
	}
}

func TestNULBytesAreStrippedFromEveryRepoWrite(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	ctx := context.Background()
	_ = s.KV().Set(ctx, "sc\x00ope", "ke\x00y", `"va\u0000lue"`)
	_, _ = s.Combos().Create(ctx, "na\x00me", "", []string{"mo\x00del"})
	_, _ = s.APIKeys().Create(ctx, "na\x00me", "mach\x00ine", "sk-ke\x00y")
	_, _ = s.Nodes().CreateUnique(ctx, Node{ID: "i\x00d", Type: "ty\x00pe", Name: "na\x00me", Data: `{}`}, "")
	for _, q := range []string{
		`SELECT count(*) FROM "kv" WHERE instr(CAST("scope"||"key"||"value" AS BLOB), x'00') > 0`,
		`SELECT count(*) FROM "combos" WHERE instr(CAST("name"||"models" AS BLOB), x'00') > 0`,
		`SELECT count(*) FROM "apiKeys" WHERE instr(CAST("name"||"machineId"||"key" AS BLOB), x'00') > 0`,
		`SELECT count(*) FROM "providerNodes" WHERE instr(CAST("id"||"type"||"name" AS BLOB), x'00') > 0`,
	} {
		if n := s.one(t, q); n != "0" {
			t.Errorf("%s -> %s", q, n)
		}
	}
}

func TestSettingsChangeHookRunsOncePerCommittedWrite(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var calls int
	s.OnSettingsChange(func() { calls++ })

	if err := s.Settings().Patch(ctx, map[string]string{"alpha": `1`}); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().MergeObjectField(ctx, "providerOverrides", "openai", `{"a":"b"}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().SetPasswordHash(ctx, "$2a$10$abc"); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().SetPasswordHash(ctx, "$2a$10$def"); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("hook ran %d times for 4 writes", calls)
	}
}

func TestSettingsChangeHookStaysQuietWhenNothingIsWritten(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().SetPasswordHash(ctx, "$2a$10$abc"); err != nil {
		t.Fatal(err)
	}
	var calls int
	s.OnSettingsChange(func() { calls++ })

	if err := s.Settings().Patch(ctx, map[string]string{"password": `"x"`}); err == nil {
		t.Fatal("a protected key was patched")
	}
	if err := s.Settings().Patch(ctx, map[string]string{"alpha": `not json`}); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
	if calls != 0 {
		t.Fatalf("hook ran %d times with no committed write", calls)
	}
}

func TestACorruptSettingsRowIsReportedAndNotRewritten(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	s.exec(t, `INSERT INTO "settings"("id","data") VALUES (1,'{broken')`)
	if _, err := s.Settings().Get(ctx); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("Get err = %v", err)
	}
	if err := s.Settings().Patch(ctx, map[string]string{"a": `1`}); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("Patch err = %v", err)
	}
	if raw := s.one(t, `SELECT "data" FROM "settings"`); raw != "{broken" {
		t.Fatalf("a corrupt settings row was rewritten: %q", raw)
	}
}

func TestAccessAllowKeepsTheFirstSpellingAndTheOrder(t *testing.T) {
	got := normalizeAllow([]string{"B/x", "a/y", "b/X", "  a/Y ", "", "c/z"})
	want := []string{"B/x", "a/y", "c/z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeAllow = %v, want %v", got, want)
	}
}

func TestAnAllowListOnAnUnrestrictedKeyIsIgnoredAndCleared(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	k, _ := s.APIKeys().Create(ctx, "x", "m", "sk-m-aaaaaa-bbbbbbbb")
	s.exec(t, `UPDATE "apiKeys" SET "accessRestricted"=0, "accessAllow"='["cx/gpt-5"]' WHERE "id"=?`, k.ID)
	got, _, _ := s.APIKeys().GetByID(ctx, k.ID)
	if got.AccessRestricted || len(got.AccessAllow) != 0 {
		t.Fatalf("an unrestricted key reads no allow list: %+v", got)
	}
	restrict := KeyAccess{Restricted: true, Allow: []string{"a/b"}}
	if _, err := s.APIKeys().Update(ctx, k.ID, APIKeyPatch{Access: &restrict}); err != nil {
		t.Fatal(err)
	}
	lift := KeyAccess{Restricted: false, Allow: []string{"a/b"}}
	upd, err := s.APIKeys().Update(ctx, k.ID, APIKeyPatch{Access: &lift})
	if err != nil || upd.AccessRestricted || len(upd.AccessAllow) != 0 {
		t.Fatalf("lifting the restriction must drop the list even when one is sent: %+v %v", upd, err)
	}
	if raw := s.one(t, `SELECT "accessAllow" FROM "apiKeys" WHERE "id"=?`, k.ID); raw != "[]" {
		t.Fatalf("stored accessAllow = %q, want []", raw)
	}
}

func TestARestrictedKeyWithAnUnparsableListDoesNotLetAnActiveToggleAddAccess(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	k, _ := s.APIKeys().Create(ctx, "x", "m", "sk-m-aaaaaa-bbbbbbbb")
	s.exec(t, `UPDATE "apiKeys" SET "accessRestricted"=1, "accessAllow"='not json' WHERE "id"=?`, k.ID)
	off := false
	upd, err := s.APIKeys().Update(ctx, k.ID, APIKeyPatch{IsActive: &off})
	if err != nil || !upd.AccessRestricted || len(upd.AccessAllow) != 0 {
		t.Fatalf("%+v %v", upd, err)
	}
	if got := s.one(t, `SELECT "accessRestricted" FROM "apiKeys" WHERE "id"=?`, k.ID); got != "1" {
		t.Fatalf("accessRestricted = %s: an unrelated update lifted the restriction", got)
	}
}

func TestADeleteReportsWhatItRemoved(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if ok, err := s.APIKeys().Delete(ctx, "nope"); err != nil || ok {
		t.Fatalf("key: %v %v", ok, err)
	}
	if ok, err := s.Combos().Delete(ctx, "nope"); err != nil || ok {
		t.Fatalf("combo: %v %v", ok, err)
	}
}

func TestEveryPatchPathRunsTheChangeHook(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var calls int
	s.OnSettingsChange(func() { calls++ })
	if err := s.Settings().SetPasswordHash(ctx, "h"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("SetPasswordHash ran the hook %d times", calls)
	}
	if err := s.Settings().Patch(ctx, map[string]string{"a": `1`}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("Patch ran the hook %d times in total", calls)
	}
	s.OnSettingsChange(func() { calls += 10 })
	if err := s.Settings().Patch(ctx, map[string]string{"a": `2`}); err != nil {
		t.Fatal(err)
	}
	if calls != 13 {
		t.Fatalf("both hooks must run on a write, calls = %d", calls)
	}
}

func TestAnImportedDatabaseKeepsWhatThisProgramNoLongerOwns(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	s.exec(t, `CREATE TABLE "proxyPools" ("id" TEXT PRIMARY KEY, "isActive" INTEGER DEFAULT 1, "testStatus" TEXT, "data" TEXT NOT NULL, "createdAt" TEXT NOT NULL, "updatedAt" TEXT NOT NULL)`)
	s.exec(t, `INSERT INTO "proxyPools"("id","data","createdAt","updatedAt") VALUES('pool','{"proxyUrl":"http://p.test"}','t','t')`)
	if err := s.Settings().Patch(ctx, map[string]string{"outboundProxyUrl": `"http://proxy:3128"`, "outboundProxyEnabled": `true`}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrating a database that holds a table of ours that was retired: %v", err)
	}
	if got := s.one(t, `SELECT "data" FROM "proxyPools" WHERE "id"='pool'`); got != `{"proxyUrl":"http://p.test"}` {
		t.Fatalf("the table's row was changed: %s", got)
	}
	eff, err := s.Settings().Effective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := eff.GetRaw("outboundProxyUrl"); v != `"http://proxy:3128"` {
		t.Fatalf("a stored value was lost: %s", v)
	}
	fresh := newMigrated(t)
	fe, _ := fresh.Settings().Effective(ctx)
	for _, k := range []string{"outboundProxyEnabled", "outboundProxyUrl", "outboundNoProxy"} {
		if fe.Has(k) {
			t.Errorf("a fresh database gets the retired setting %s", k)
		}
	}
	if fresh.one(t, `SELECT COUNT(*) FROM sqlite_master WHERE name='proxyPools'`) != "0" && !tests.UsingPostgres() {
		t.Error("a fresh database creates the retired table")
	}
}
