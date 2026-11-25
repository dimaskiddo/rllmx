package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"rllmx/app/access/identity"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/cache"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

const (
	alpha = `{"id":"alpha","alias":"al","category":"apikey","display":{"name":"A"},"transport":{"baseUrl":"https://a.example/v1/chat/completions","format":"openai"},"models":[{"id":"a1","name":"A One"},{"id":"a2"}]}`
	codex = `{"id":"codex","alias":"cx","category":"apikey","display":{"name":"C"},"transport":{"baseUrl":"https://c.example/v1/chat/completions","format":"openai"},"models":[{"id":"gpt-a"},{"id":"gpt-b"}]}`
	nodeA = "openai-compatible-n1"
)

type fakeKV struct {
	scopes   map[string]map[string]string
	disabled map[string][]string
	err      error
}

func (k *fakeKV) List(_ context.Context, scope string) (map[string]string, error) {
	return k.scopes[scope], k.err
}
func (k *fakeKV) DisabledModels(_ context.Context, p string) ([]string, error) {
	return k.disabled[p], k.err
}

type fakeConns struct {
	list []repository.Connection
	err  error
}

func (f *fakeConns) List(_ context.Context, flt repository.ConnFilter) ([]repository.Connection, error) {
	var out []repository.Connection
	for _, c := range f.list {
		if flt.IsActive != nil && c.IsActive != *flt.IsActive {
			continue
		}
		out = append(out, c)
	}
	return out, f.err
}

type fakeNodes struct{ list []repository.Node }

func (f *fakeNodes) List(context.Context, string) ([]repository.Node, error) { return f.list, nil }

type rig struct {
	t     *testing.T
	c     *Catalog
	kv    *fakeKV
	conns *fakeConns
	nodes *fakeNodes
	asked atomic.Int32
	reply func(nodeType, base, key string) ([]Model, error)
}

func conn(id, provider, data string) repository.Connection {
	return repository.Connection{ID: id, Provider: provider, IsActive: true, Data: data}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	reg, err := upstream.Load(fstest.MapFS{"data/alpha.json": {Data: []byte(alpha)}, "data/codex.json": {Data: []byte(codex)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, kv: &fakeKV{scopes: map[string]map[string]string{}, disabled: map[string][]string{}}, conns: &fakeConns{}, nodes: &fakeNodes{}}
	r.c = &Catalog{
		Registry: reg, Caps: caps, Conns: r.conns, Nodes: r.nodes, KV: r.kv, Created: 1700000000,
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(routingSource{r}, nil)},
		Fetch: func(_ context.Context, nt, base, key string) ([]Model, error) {
			r.asked.Add(1)
			if r.reply == nil {
				return nil, errors.New("down")
			}
			return r.reply(nt, base, key)
		},
		Cache: cache.New(tests.NewCache()),
	}
	return r
}

type routingSource struct{ r *rig }

func (s routingSource) Aliases(context.Context) (map[string]string, error) {
	return s.r.kv.scopes[repository.ScopeModelAliases], nil
}
func (s routingSource) Nodes(context.Context) ([]routing.Node, error) {
	var out []routing.Node
	for _, n := range s.r.nodes.list {
		p, _ := n.Doc().GetString("prefix")
		out = append(out, routing.Node{ID: n.ID, Type: n.Type, Prefix: p})
	}
	return out, nil
}
func (s routingSource) ComboNames(context.Context) ([]string, error) { return nil, nil }
func (s routingSource) Combos(context.Context) (map[string]repository.Combo, error) {
	return nil, nil
}
func (s routingSource) ActiveProviders(context.Context) ([]string, error) {
	return nil, nil
}

func (r *rig) custom(key, value string) {
	if r.kv.scopes[repository.ScopeCustomModels] == nil {
		r.kv.scopes[repository.ScopeCustomModels] = map[string]string{}
	}
	r.kv.scopes[repository.ScopeCustomModels][key] = value
}

func (r *rig) ids(caller identity.Caller) []string {
	r.t.Helper()
	list, err := r.c.List(context.Background(), caller)
	if err != nil {
		r.t.Fatal(err)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.ID)
	}
	return out
}

func has(list []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, g := range list {
			found = found || g == w
		}
		if !found {
			return false
		}
	}
	return true
}

var anyone = identity.Caller{ID: "k", Name: "k"}

func TestAnEmptyGatewayListsNothing(t *testing.T) {
	r := newRig(t)
	if got := r.ids(anyone); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

func TestOnlyAProviderWithAnUsableConnectionIsListed(t *testing.T) {
	r := newRig(t)
	inactive := conn("c2", "codex", `{"apiKey":"k"}`)
	inactive.IsActive = false
	corrupt := conn("c3", "codex", `{}`)
	corrupt.Corrupt = true
	expired := conn("c4", "codex", `{"testStatus":"expired"}`)
	r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`), inactive, corrupt, expired}
	got := r.ids(anyone)
	if !has(got, "al/a1", "al/a2") || len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

// A provider that needs no credential is offered with nothing connected: the
// request reaches it through a virtual account.
func TestAProviderThatNeedsNoCredentialIsListedWithNothingConnected(t *testing.T) {
	const freeLane = `{"id":"free","category":"free","noAuth":true,"display":{"name":"F"},` +
		`"transport":{"baseUrl":"https://f.example/v1/chat/completions","format":"openai"},"models":[{"id":"fm"}]}`
	reg, err := upstream.Load(fstest.MapFS{"data/free.json": {Data: []byte(freeLane)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, kv: &fakeKV{scopes: map[string]map[string]string{}, disabled: map[string][]string{}}, conns: &fakeConns{}, nodes: &fakeNodes{}}
	r.c = &Catalog{
		Registry: reg, Caps: caps, Conns: r.conns, Nodes: r.nodes, KV: r.kv, Created: 1700000000,
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(routingSource{r}, nil)},
	}
	if got := r.ids(anyone); len(got) != 1 || got[0] != "free/fm" {
		t.Fatalf("%v", got)
	}
}

func TestAProviderWithNoConnectionIsAbsentWhileAnotherIsListed(t *testing.T) {
	r := newRig(t)
	r.conns.list = []repository.Connection{conn("c1", "codex", `{"apiKey":"k"}`)}
	got := r.ids(anyone)
	if len(got) != 2 || !has(got, "cx/gpt-a", "cx/gpt-b") {
		t.Fatalf("%v", got)
	}
}

func TestEveryConnectionCountsAndAnEnabledListHidesTheRest(t *testing.T) {
	r := newRig(t)
	only := conn("c1", "codex", `{"providerSpecificData":{"enabledModels":["gpt-a"]}}`)
	r.conns.list = []repository.Connection{only}
	if got := r.ids(anyone); len(got) != 1 || got[0] != "cx/gpt-a" {
		t.Fatalf("%v", got)
	}
	r.conns.list = append(r.conns.list, conn("c2", "codex", `{"apiKey":"k"}`))
	if got := r.ids(anyone); !has(got, "cx/gpt-a", "cx/gpt-b") {
		t.Fatalf("a second connection that accepts everything was ignored: %v", got)
	}
}

func TestCustomDisabledAndAliasModelsAreMerged(t *testing.T) {
	r := newRig(t)
	r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`)}
	r.custom("alpha|own|llm", `{"id":"own","name":"Own","type":"llm"}`)
	r.custom("al|viaalias|llm", `{"id":"viaalias","type":"llm"}`)
	r.custom("alpha|img|image", `{"id":"img","type":"image"}`)
	r.custom("alpha|a1|llm", `{"id":"a1","name":"Replaced"}`)
	r.custom("other|x|llm", `{"id":"x"}`)
	r.kv.disabled["alpha"] = []string{"a2"}
	r.kv.scopes[repository.ScopeModelAliases] = map[string]string{"fast": `"alpha/viaalias2"`, "bad": `"nope"`}
	got := r.ids(anyone)
	want := []string{"al/a1", "al/own", "al/viaalias", "al/viaalias2"}
	if len(got) != len(want) || !has(got, want...) {
		t.Fatalf("%v, want %v", got, want)
	}
	list, _ := r.c.List(context.Background(), anyone)
	if list[0].Name != "Replaced" {
		t.Fatalf("a custom row did not replace the built-in entry: %+v", list[0])
	}
}

// A custom row the dashboard adds usually repeats the id as its name, because
// the name is optional there. Replacing a built-in row must not lose the
// description the registry carries.
func TestACustomRowWithoutANameKeepsTheBuiltInDescription(t *testing.T) {
	for _, name := range []string{`{"id":"a1","type":"llm"}`, `{"id":"a1","name":"a1","type":"llm"}`} {
		r := newRig(t)
		r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`)}
		r.custom("alpha|a1|llm", name)
		list, _ := r.c.List(context.Background(), anyone)
		if list[0].Model != "a1" || list[0].Name != "A One" {
			t.Fatalf("%s: %+v, want the built-in name", name, list[0])
		}
	}
	r := newRig(t)
	r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`)}
	r.custom("alpha|a1|llm", `{"id":"a1","name":"Own Name","type":"llm"}`)
	list, _ := r.c.List(context.Background(), anyone)
	if list[0].Name != "Own Name" {
		t.Fatalf("a typed name was replaced: %+v", list[0])
	}
}

func TestIdsAreBoundedAndFreeOfControlCharacters(t *testing.T) {
	r := newRig(t)
	r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`)}
	r.custom("alpha|"+strings.Repeat("x", 300)+"|llm", `{}`)
	r.custom("alpha|bad\x07id|llm", `{}`)
	r.custom("alpha|ok|llm", `{"name":"bad\u0007name"}`)
	if got := r.ids(anyone); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestEveryListedIdResolvesBackToItsOwnModel(t *testing.T) {
	r := newRig(t)
	r.nodes.list = []repository.Node{
		{ID: nodeA, Type: upstream.NodeTypeOpenAI, Data: `{"prefix":"my","baseUrl":"https://n.example/v1"}`},
		{ID: "openai-compatible-n2", Type: upstream.NodeTypeOpenAI, Data: `{"baseUrl":"https://m.example/v1"}`},
	}
	r.conns.list = []repository.Connection{
		conn("c1", "alpha", `{"apiKey":"k"}`),
		conn("c2", nodeA, `{"apiKey":"k","providerSpecificData":{"enabledModels":["m1"]}}`),
		conn("c3", "openai-compatible-n2", `{"apiKey":"k","providerSpecificData":{"enabledModels":["m2"]}}`),
	}
	list, err := r.c.List(context.Background(), anyone)
	if err != nil || len(list) != 4 {
		t.Fatalf("%v %v", list, err)
	}
	for _, e := range list {
		got, err := r.c.Resolver.Resolve(context.Background(), e.ID)
		if err != nil || got.Provider != e.Provider || got.Model != e.Model {
			t.Errorf("%s resolves to %+v (%v), want %s/%s", e.ID, got, err, e.Provider, e.Model)
		}
	}
	if !has(r.ids(anyone), "my/m1", "openai-compatible-n2/m2") {
		t.Fatalf("%v", r.ids(anyone))
	}
}

func nodeRig(t *testing.T) *rig {
	r := newRig(t)
	r.nodes.list = []repository.Node{{ID: nodeA, Type: upstream.NodeTypeOpenAI, Data: `{"prefix":"my","baseUrl":"https://n.example/v1"}`}}
	r.conns.list = []repository.Connection{conn("c1", nodeA, `{"apiKey":"secret-key"}`)}
	return r
}

func TestANodeIsAskedOnceAndAgainWhenItsAddressChanges(t *testing.T) {
	r := nodeRig(t)
	var key string
	r.reply = func(_, _, k string) ([]Model, error) { key = k; return []Model{{ID: "m1"}}, nil }
	for i := 0; i < 3; i++ {
		if got := r.ids(anyone); len(got) != 1 || got[0] != "my/m1" {
			t.Fatalf("%v", got)
		}
	}
	if r.asked.Load() != 1 || key != "secret-key" {
		t.Fatalf("asked %d times with %q", r.asked.Load(), key)
	}
	r.nodes.list[0].Data = `{"prefix":"my","baseUrl":"https://moved.example/v1"}`
	r.ids(anyone)
	if r.asked.Load() != 2 {
		t.Fatalf("an edited address was not asked again: %d", r.asked.Load())
	}
}

func TestANodeThatFailsAddsNothingAndCustomModelsStay(t *testing.T) {
	r := nodeRig(t)
	r.custom("my|c1|llm", `{"id":"c1"}`)
	got := r.ids(anyone)
	if len(got) != 1 || got[0] != "my/c1" {
		t.Fatalf("%v", got)
	}
	r.ids(anyone)
	if r.asked.Load() != 1 {
		t.Fatalf("a down node was asked on every request: %d", r.asked.Load())
	}
}

func TestACachedNodeListNeverHoldsTheKey(t *testing.T) {
	facade := tests.NewCache()
	r := nodeRig(t)
	r.c.Cache = cache.New(facade)
	r.reply = func(_, _, _ string) ([]Model, error) { return []Model{{ID: "m1"}}, nil }
	r.ids(anyone)
	for _, name := range []string{"memory", "redis"} {
		s := facade.StoreNamed(name)
		for _, k := range s.Keys("") {
			if strings.Contains(k, "secret-key") || strings.Contains(fmt.Sprint(s.Get(k)), "secret-key") {
				t.Fatalf("the %s store holds the key under %q", name, k)
			}
		}
	}
}

func TestAnEmbeddingNodeIsNeverListed(t *testing.T) {
	r := newRig(t)
	r.nodes.list = []repository.Node{{ID: "custom-embedding-e1", Type: upstream.NodeTypeEmbedding, Data: `{"baseUrl":"https://e.example"}`}}
	r.conns.list = []repository.Connection{conn("c1", "custom-embedding-e1", `{"apiKey":"k","providerSpecificData":{"enabledModels":["e"]}}`)}
	if got := r.ids(anyone); len(got) != 0 || r.asked.Load() != 0 {
		t.Fatalf("%v asked %d", got, r.asked.Load())
	}
}

func TestARestrictedKeySeesOnlyItsModelsWrittenInAnyForm(t *testing.T) {
	r := nodeRig(t)
	r.conns.list = append(r.conns.list, conn("c2", "alpha", `{"apiKey":"k"}`))
	r.reply = func(_, _, _ string) ([]Model, error) { return []Model{{ID: "m1"}, {ID: "m2"}}, nil }
	for _, allow := range [][]string{{"al/a1"}, {"alpha/a1"}} {
		got := r.ids(identity.Caller{Restricted: true, Allow: allow})
		if len(got) != 1 || got[0] != "al/a1" {
			t.Errorf("allow %v: %v", allow, got)
		}
	}
	got := r.ids(identity.Caller{Restricted: true, Allow: []string{"my/m2", "nope/x", "", "a-combo"}})
	if len(got) != 1 || got[0] != "my/m2" {
		t.Errorf("%v", got)
	}
	if got := r.ids(identity.Caller{Restricted: true}); len(got) != 0 {
		t.Errorf("a restricted key with no list sees %v", got)
	}
	if got := r.ids(identity.Caller{Restricted: true, Allow: []string{"AL/A1"}}); len(got) != 0 {
		t.Errorf("a spelling the resolver does not map was matched: %v", got)
	}
	if got := r.ids(identity.Caller{Anonymous: true}); len(got) != 4 {
		t.Errorf("%v", got)
	}
}

func TestAFailedStoreReadFailsTheList(t *testing.T) {
	r := newRig(t)
	r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`)}
	r.kv.err = errors.New("db down")
	if _, err := r.c.List(context.Background(), anyone); err == nil {
		t.Fatal("a failed read was treated as empty")
	}
	r.kv.err = nil
	r.conns.err = errors.New("db down")
	if _, err := r.c.List(context.Background(), anyone); err == nil {
		t.Fatal("a failed read of connections was treated as empty")
	}
}

func TestGetFindsOnlyAListedModelAndEntriesRenderEveryShape(t *testing.T) {
	r := newRig(t)
	r.conns.list = []repository.Connection{conn("c1", "alpha", `{"apiKey":"k"}`)}
	ctx := context.Background()
	e, ok, err := r.c.Get(ctx, anyone, "al/a1")
	if err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	if _, ok, _ := r.c.Get(ctx, anyone, "al/zzz"); ok {
		t.Fatal("an unlisted model was found")
	}
	if _, ok, _ := r.c.Get(ctx, identity.Caller{Restricted: true, Allow: []string{"al/a2"}}, "al/a1"); ok {
		t.Fatal("a model outside the key's list was found")
	}
	o := e.OpenAI(r.c.Created)
	if o.Object != "model" || o.Created != 1700000000 || o.OwnedBy != "al" || o.ContextLength <= 0 || o.MaxCompletionTokens <= 0 {
		t.Fatalf("%+v", o)
	}
	if i := e.Info(); i.Kind != "llm" || i.Endpoint != "/v1/chat/completions" || i.Name != "A One" {
		t.Fatalf("%+v", i)
	}
	if g := e.Gemini(); g.Name != "models/al/a1" || g.InputTokenLimit != o.ContextLength || len(g.SupportedGenerationMethods) != 2 {
		t.Fatalf("%+v", g)
	}
}

func FuzzRegistryModels(f *testing.F) {
	f.Add("alpha|x|llm", `{"id":"y","name":"n"}`)
	f.Add("al|"+strings.Repeat("z", 400), `{`)
	f.Add("alpha|a\x00b|image", `{"id":"\u0007"}`)
	reg, err := upstream.Load(fstest.MapFS{"data/alpha.json": {Data: []byte(alpha)}}, "data")
	if err != nil {
		f.Fatal(err)
	}
	e, _ := reg.ByID("alpha")
	f.Fuzz(func(t *testing.T, key, value string) {
		kv := &fakeKV{scopes: map[string]map[string]string{repository.ScopeCustomModels: {key: value}}}
		list, err := RegistryModels(context.Background(), kv, e)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, m := range list {
			if m.ID == "" || len(m.ID) > maxIDLen || !clean(m.ID) || !clean(m.Name) || seen[m.ID] {
				t.Fatalf("bad entry %+v", m)
			}
			seen[m.ID] = true
		}
	})
}

func TestTheModelListPublishesThinkingLevels(t *testing.T) {
	e := Entry{ID: "claude/claude-opus-5", OwnedBy: "claude", Caps: upstream.Capability{Reasoning: true}, Levels: []string{"low", "medium", "high"}}
	if got := e.OpenAI(0).ThinkingLevels; len(got) != 3 || got[2] != "high" {
		t.Fatalf("%v", got)
	}
	raw, _ := json.Marshal(e.OpenAI(0))
	if !strings.Contains(string(raw), `"thinking_levels":["low","medium","high"]`) {
		t.Fatalf("%s", raw)
	}
	// A model with no reasoning carries no field.
	plain := Entry{ID: "p/m", OwnedBy: "p"}
	raw, _ = json.Marshal(plain.OpenAI(0))
	if strings.Contains(string(raw), "thinking_levels") {
		t.Fatalf("%s", raw)
	}
}

// A live list replaces the embedded rows and keeps the operator's own models,
// minus the switched-off ids.
func TestALiveListKeepsCustomRowsAndDropsDisabledOnes(t *testing.T) {
	r := newRig(t)
	r.kv.scopes[repository.ScopeCustomModels] = map[string]string{"codex|mine|llm": `{"providerAlias":"codex","id":"mine","name":"Mine"}`}
	r.kv.disabled["codex"] = []string{"gone"}
	entry, ok := r.c.Registry.ByID("codex")
	if !ok {
		t.Fatal("codex missing")
	}
	list, err := LiveList(context.Background(), r.kv, entry, []string{"live-1", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, m := range list {
		ids[m.ID] = m.Source
	}
	if ids["live-1"] != "live" || ids["mine"] != "custom" {
		t.Fatalf("%v", ids)
	}
	if _, found := ids["gone"]; found {
		t.Fatal("a disabled id survived the live list")
	}
	if _, found := ids["gpt-a"]; found {
		t.Fatal("an embedded id survived the live list")
	}
}

func TestAnImageModelOfAChatProviderIsNotListedAsAChatModel(t *testing.T) {
	const mixed = `{"id":"free","category":"free","noAuth":true,"display":{"name":"F"},` +
		`"transport":{"baseUrl":"https://f.example/v1/chat/completions","format":"openai"},` +
		`"models":[{"id":"fm"},{"id":"draw","kind":"image"}]}`
	reg, err := upstream.Load(fstest.MapFS{"data/free.json": {Data: []byte(mixed)}}, "data")
	if err != nil {
		t.Fatal(err)
	}
	caps, err := upstream.LoadCapabilities(reg)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, kv: &fakeKV{scopes: map[string]map[string]string{}, disabled: map[string][]string{}}, conns: &fakeConns{}, nodes: &fakeNodes{}}
	r.c = &Catalog{
		Registry: reg, Caps: caps, Conns: r.conns, Nodes: r.nodes, KV: r.kv, Created: 1700000000,
		Resolver: &routing.Resolver{Registry: reg, Snapshot: routing.NewSnapshot(routingSource{r}, nil)},
	}
	if got := r.ids(anyone); len(got) != 1 || got[0] != "free/fm" {
		t.Fatalf("%v", got)
	}
}
