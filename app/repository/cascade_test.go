package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"rllmx/app/document"
	"rllmx/tests"
)

func TestPatchPSDSetsAndRemovesKeysAndKeepsTheRest(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "bedrock", AuthType: "apikey", Name: "x", PSD: `{"region":"us-east-1","sessionToken":"tok","keep":1}`})
	err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"region": `"eu-west-1"`, "profile": `"dev"`}, []string{"sessionToken", "absent"})
	if err != nil {
		t.Fatal(err)
	}
	d := psdOfConn(t, s, a.ID)
	if v, _ := d.GetString("region"); v != "eu-west-1" {
		t.Errorf("region %q", v)
	}
	if v, _ := d.GetString("profile"); v != "dev" {
		t.Errorf("profile %q", v)
	}
	if d.Has("sessionToken") {
		t.Errorf("sessionToken was not removed: %s", d.String())
	}
	if v, _ := d.GetInt("keep"); v != 1 {
		t.Errorf("an untouched key was lost: %s", d.String())
	}
}

func TestPatchPSDCreatesTheObjectWhenThereIsNone(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x"})
	if err := s.Connections().PatchPSD(context.Background(), a.ID, map[string]string{"baseUrl": `"https://x.example"`}, nil); err != nil {
		t.Fatal(err)
	}
	if v, _ := psdOfConn(t, s, a.ID).GetString("baseUrl"); v != "https://x.example" {
		t.Fatalf("%q", v)
	}
}

func TestPatchPSDLeavesEveryOtherFieldOfTheRowAlone(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x", Fields: map[string]string{"apiKey": `"sk-1"`}})
	mustLock(t, s, a.ID, "m", mustTime(t, "2999-01-01T00:00:00.000Z"), 429, "r", 1)
	before, _, _ := s.Connections().Get(ctx, a.ID)
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"x": `1`}, nil); err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.Connections().Get(ctx, a.ID)
	for _, k := range []string{"apiKey", "modelLock_m", "testStatus", "backoffLevel", "errorCode"} {
		b, _ := before.Doc().GetRaw(k)
		a2, _ := after.Doc().GetRaw(k)
		if b != a2 {
			t.Errorf("%s changed: %s -> %s", k, b, a2)
		}
	}
}

func TestPatchPSDRejectsBadInput(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x"})
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"k": `{not json`}, nil); err == nil {
		t.Error("an invalid JSON value was accepted")
	}
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"": `1`}, nil); err == nil {
		t.Error("an empty key was accepted")
	}
	if err := s.Connections().PatchPSD(ctx, "nope", map[string]string{"k": `1`}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing row: %v", err)
	}
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"k": `1`}, []string{"k"}); err == nil {
		t.Error("setting and removing one key in one call is ambiguous and must be refused")
	}
}

func TestPatchPSDRefusesACorruptRowAndLeavesItAlone(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x"})
	s.exec(t, `UPDATE "providerConnections" SET "data" = ? WHERE "id" = ?`, "{broken", a.ID)
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"k": `1`}, nil); !errors.Is(err, document.ErrCorruptRow) {
		t.Fatalf("%v", err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if got.Data != "{broken" {
		t.Fatalf("a corrupt row was rewritten: %q", got.Data)
	}
}

func TestPatchPSDDoesNotLoseConcurrentWritesToDifferentKeys(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x"})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{fmt.Sprintf("k%d", i): `true`}, nil); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	d := psdOfConn(t, s, a.ID)
	for i := range 20 {
		if !d.Has(fmt.Sprintf("k%d", i)) {
			t.Fatalf("key k%d was lost: %s", i, d.String())
		}
	}
}

func TestPatchPSDRefusesARawNULAndNeverStoresOne(t *testing.T) {
	tests.RequireSQLite(t)
	s := newMigrated(t)
	ctx := context.Background()
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x"})
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"k": "\"a\x00b\""}, nil); err != nil {
		t.Logf("a raw NUL byte inside a JSON string is refused or stripped: %v", err)
	}
	if err := s.Connections().PatchPSD(ctx, a.ID, map[string]string{"k": `"a\u0000b"`}, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.Connections().Get(ctx, a.ID)
	if strings.ContainsRune(got.Data, 0) {
		t.Fatal("a NUL byte reached the database")
	}
	if n := s.one(t, `SELECT count(*) FROM "providerConnections" WHERE instr(CAST("data" AS BLOB), x'00') > 0`); n != "0" {
		t.Fatal("a NUL byte reached the stored column")
	}
}

func TestPatchPSDRefusesAnEmptyKeyEvenWithAValidValue(t *testing.T) {
	s := newMigrated(t)
	a := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "x"})
	err := s.Connections().PatchPSD(context.Background(), a.ID, map[string]string{"": `1`}, nil)
	if err == nil || !strings.Contains(err.Error(), "key") {
		t.Fatalf("%v", err)
	}
	got, _, _ := s.Connections().Get(context.Background(), a.ID)
	if strings.Contains(got.Data, `"":`) {
		t.Fatalf("an empty key was written: %s", got.Data)
	}
}

func TestMergeObjectFieldChangesOneFieldAndKeepsTheOthers(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().MergeObjectField(ctx, "providerOverrides", "claude", `{"headers":{"X-A":"1"}}`); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().MergeObjectField(ctx, "providerOverrides", "codex", `{"headers":{"X-B":"2"}}`); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Settings().Get(ctx)
	raw, _ := d.GetRaw("providerOverrides")
	o := mustDoc(t, raw)
	if !o.Has("claude") || !o.Has("codex") {
		t.Fatalf("a field was lost: %s", raw)
	}
	if err := s.Settings().MergeObjectField(ctx, "providerOverrides", "claude", `null`); err != nil {
		t.Fatal(err)
	}
	d, _ = s.Settings().Get(ctx)
	raw, _ = d.GetRaw("providerOverrides")
	o = mustDoc(t, raw)
	if o.Has("claude") || !o.Has("codex") {
		t.Fatalf("null must delete exactly one field: %s", raw)
	}
}

func TestMergeObjectFieldKeepsOtherSettingsAndCreatesTheObject(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().Patch(ctx, map[string]string{"requireLogin": `false`, "futureKey": `{"a":1}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().MergeObjectField(ctx, "providerOverrides", "claude", `{"headers":{}}`); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Settings().Get(ctx)
	if v, _ := d.GetRaw("requireLogin"); v != "false" {
		t.Errorf("requireLogin %q", v)
	}
	if v, _ := d.GetRaw("futureKey"); v != `{"a":1}` {
		t.Errorf("an unknown key was changed: %q", v)
	}
}

func TestMergeObjectFieldReplacesANonObjectValue(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().Patch(ctx, map[string]string{"providerOverrides": `"oops"`}); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings().MergeObjectField(ctx, "providerOverrides", "claude", `{"headers":{}}`); err != nil {
		t.Fatal(err)
	}
	d, _ := s.Settings().Get(ctx)
	raw, _ := d.GetRaw("providerOverrides")
	if o, err := document.ParseDoc(raw); err != nil || !o.Has("claude") {
		t.Fatalf("%q %v", raw, err)
	}
}

func TestMergeObjectFieldRefusesProtectedAndBadInput(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if err := s.Settings().MergeObjectField(ctx, "password", "x", `1`); !errors.Is(err, ErrProtectedSetting) {
		t.Errorf("a protected setting was written: %v", err)
	}
	cases := map[string][3]string{
		"empty key":     {"", "f", `1`},
		"empty field":   {"providerOverrides", "", `1`},
		"invalid value": {"providerOverrides", "f", `{bad`},
		"empty value":   {"providerOverrides", "f", ``},
	}
	for name, c := range cases {
		if err := s.Settings().MergeObjectField(ctx, c[0], c[1], c[2]); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	d, _ := s.Settings().Get(ctx)
	if d.Has("providerOverrides") || d.Has("") {
		t.Fatalf("a rejected call wrote something: %s", d.String())
	}
}

func TestMergeObjectFieldDoesNotLoseConcurrentWrites(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Settings().MergeObjectField(ctx, "providerOverrides", fmt.Sprintf("p%d", i), `{"headers":{}}`); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	d, _ := s.Settings().Get(ctx)
	raw, _ := d.GetRaw("providerOverrides")
	o := mustDoc(t, raw)
	for i := range 20 {
		if !o.Has(fmt.Sprintf("p%d", i)) {
			t.Fatalf("field p%d lost: %s", i, raw)
		}
	}
}

func TestUpdateCascadeChangesTheNodeAndEveryConnectionOfIt(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	mkNode(t, s, "openai-compatible-chat-1", "openai-compatible", `{"prefix":"old","baseUrl":"https://old.example","apiType":"chat"}`)
	c1 := mkConn(t, s, ConnInput{Provider: "openai-compatible-chat-1", AuthType: "apikey", Name: "a", PSD: `{"prefix":"old","baseUrl":"https://old.example","nodeName":"Old","extra":"keep"}`})
	c2 := mkConn(t, s, ConnInput{Provider: "openai-compatible-chat-1", AuthType: "apikey", Name: "b"})
	other := mkConn(t, s, ConnInput{Provider: "openai", AuthType: "apikey", Name: "c", PSD: `{"prefix":"old"}`})

	n, err := s.Nodes().UpdateCascade(ctx, "openai-compatible-chat-1",
		map[string]string{"name": `"New"`, "prefix": `"fresh"`, "baseUrl": `"https://new.example"`},
		map[string]string{"prefix": `"fresh"`, "baseUrl": `"https://new.example"`, "nodeName": `"New"`}, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "New" {
		t.Errorf("node name %q", n.Name)
	}
	for _, id := range []string{c1.ID, c2.ID} {
		d := psdOfConn(t, s, id)
		if v, _ := d.GetString("prefix"); v != "fresh" {
			t.Errorf("%s prefix %q", id, v)
		}
		if v, _ := d.GetString("baseUrl"); v != "https://new.example" {
			t.Errorf("%s baseUrl %q", id, v)
		}
	}
	if v, _ := psdOfConn(t, s, c1.ID).GetString("extra"); v != "keep" {
		t.Error("another key of providerSpecificData was lost")
	}
	if v, _ := psdOfConn(t, s, other.ID).GetString("prefix"); v != "old" {
		t.Error("a connection of another provider was changed")
	}
}

func TestUpdateCascadeIsAllOrNothing(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	mkNode(t, s, "n1", "openai-compatible", `{"prefix":"old"}`)
	good := mkConn(t, s, ConnInput{Provider: "n1", AuthType: "apikey", Name: "good", PSD: `{"prefix":"old"}`})
	bad := mkConn(t, s, ConnInput{Provider: "n1", AuthType: "apikey", Name: "bad"})
	s.exec(t, `UPDATE "providerConnections" SET "data" = ? WHERE "id" = ?`, "{broken", bad.ID)
	_, err := s.Nodes().UpdateCascade(ctx, "n1", map[string]string{"prefix": `"fresh"`}, map[string]string{"prefix": `"fresh"`}, "fresh")
	if err == nil {
		t.Fatal("expected a failure from the corrupt connection")
	}
	n, _, _ := s.Nodes().Get(ctx, "n1")
	if v, _ := mustDoc(t, n.Data).GetString("prefix"); v != "old" {
		t.Errorf("the node changed although the cascade failed: %q", v)
	}
	if v, _ := psdOfConn(t, s, good.ID).GetString("prefix"); v != "old" {
		t.Errorf("a connection changed although the cascade failed: %q", v)
	}
}

func TestUpdateCascadeMissingNode(t *testing.T) {
	s := newMigrated(t)
	if _, err := s.Nodes().UpdateCascade(context.Background(), "nope", map[string]string{"name": `"x"`}, nil, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestDeleteCascadeRemovesTheNodeItsConnectionsAndItsCustomModels(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	mkNode(t, s, "n1", "openai-compatible", `{"prefix":"p"}`)
	mkNode(t, s, "n2", "openai-compatible", `{"prefix":"q"}`)
	c1 := mkConn(t, s, ConnInput{Provider: "n1", AuthType: "apikey", Name: "a"})
	c2 := mkConn(t, s, ConnInput{Provider: "n1", AuthType: "apikey", Name: "b"})
	keep := mkConn(t, s, ConnInput{Provider: "n2", AuthType: "apikey", Name: "c"})
	putCustomModel(t, s, "n1", "m")
	removed, err := s.Nodes().DeleteCascade(ctx, "n1")
	if err != nil || removed != 2 {
		t.Fatalf("%d %v", removed, err)
	}
	if _, ok, _ := s.Nodes().Get(ctx, "n1"); ok {
		t.Error("the node is still there")
	}
	for _, id := range []string{c1.ID, c2.ID} {
		if _, ok, _ := s.Connections().Get(ctx, id); ok {
			t.Errorf("connection %s survived", id)
		}
	}
	if _, ok, _ := s.Connections().Get(ctx, keep.ID); !ok {
		t.Error("a connection of another node was deleted")
	}
	if _, ok, _ := s.Nodes().Get(ctx, "n2"); !ok {
		t.Error("another node was deleted")
	}
	if _, err := s.Nodes().DeleteCascade(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing node: %v", err)
	}
}

func TestCreateUniqueRefusesATakenPrefixInTheSameTransaction(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	if _, err := s.Nodes().CreateUnique(ctx, Node{ID: "n1", Type: "openai-compatible", Name: "A", Data: `{"prefix":"lab"}`}, "lab"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Nodes().CreateUnique(ctx, Node{ID: "n2", Type: "custom-embedding", Name: "B", Data: `{"prefix":"lab"}`}, "lab")
	if !errors.Is(err, ErrPrefixTaken) {
		t.Fatalf("%v", err)
	}
	if _, ok, _ := s.Nodes().Get(ctx, "n2"); ok {
		t.Fatal("the refused node was stored")
	}
	if _, err := s.Nodes().CreateUnique(ctx, Node{ID: "n3", Type: "openai-compatible", Name: "C", Data: `{"prefix":"Lab"}`}, "Lab"); err != nil {
		t.Fatalf("prefixes are case-sensitive: %v", err)
	}
}

func TestCreateUniqueIsSafeUnderConcurrency(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Nodes().CreateUnique(ctx, Node{ID: fmt.Sprintf("n%d", i), Type: "openai-compatible", Name: "N", Data: `{"prefix":"same"}`}, "same")
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	list, _ := s.Nodes().List(ctx, "")
	if ok != 1 || len(list) != 1 {
		t.Fatalf("%d succeeded, %d stored", ok, len(list))
	}
}

func TestUpdateCascadeAllowsAnUnchangedPrefixAndRefusesAnotherNodes(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	mkNode(t, s, "a", "openai-compatible", `{"prefix":"aaa"}`)
	mkNode(t, s, "b", "openai-compatible", `{"prefix":"bbb"}`)
	if _, err := s.Nodes().UpdateCascade(ctx, "a", map[string]string{"name": `"A2"`, "prefix": `"aaa"`}, nil, "aaa"); err != nil {
		t.Fatalf("an unchanged prefix: %v", err)
	}
	_, err := s.Nodes().UpdateCascade(ctx, "a", map[string]string{"name": `"A3"`, "prefix": `"bbb"`}, nil, "bbb")
	if !errors.Is(err, ErrPrefixTaken) {
		t.Fatalf("%v", err)
	}
	n, _, _ := s.Nodes().Get(ctx, "a")
	if n.Name != "A2" {
		t.Fatalf("a refused update changed the node: %q", n.Name)
	}
}

func TestPrefixOfReadsTheStoredPrefix(t *testing.T) {
	for in, want := range map[string]string{`{"prefix":"x"}`: "x", `{"prefix":5}`: "", `{}`: "", `{broken`: "", `{"prefix":null}`: ""} {
		if got := prefixOf(in); got != want {
			t.Errorf("prefixOf(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestNodesWithoutAPrefixNeverCollide(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if _, err := s.Nodes().CreateUnique(ctx, Node{ID: id, Type: "custom-embedding", Name: id, Data: `{"baseUrl":"https://x"}`}, ""); err != nil {
			t.Fatalf("node %s without a prefix: %v", id, err)
		}
	}
	if _, err := s.Nodes().UpdateCascade(ctx, "a", map[string]string{"name": `"A2"`}, nil, ""); err != nil {
		t.Fatalf("a cascade update of a node without a prefix: %v", err)
	}
}
