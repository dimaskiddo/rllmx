package controllers

import (
	"context"
	nethttp "net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	apphttp "rllmx/app/http"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/repository"
)

func newPricingRig(t *testing.T) (*mgmtRig, *PricingController, *int) {
	t.Helper()
	m := newMgmtRig(t)
	reloads := 0
	table := func() (*upstream.Table, error) {
		base, err := upstream.DefaultTable(m.pc.Registry)
		if err != nil {
			return nil, err
		}
		raw, err := m.st.Store.KV().List(context.Background(), repository.ScopePricing)
		if err != nil {
			return nil, err
		}
		return base.WithOverrides(upstream.ParseOverrides(raw, func(string, ...any) {})), nil
	}
	c := &PricingController{DB: func() *repository.Store { return m.st.Store }, Table: table, Reload: func() { reloads++ }}
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/pricing", c.Show)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodPatch, "/api/pricing", c.Update)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodDelete, "/api/pricing", c.Reset)
	return m, c, &reloads
}

func TestAStoredPricingRowIsReadAndWrittenWithTheStoredSpelling(t *testing.T) {
	m, _, reloads := newPricingRig(t)
	ctx := context.Background()
	if err := m.st.Store.KV().Set(ctx, repository.ScopePricing, "claude",
		`{"m": {"input": 1, "output": 2, "cache_creation": 5}}`); err != nil {
		t.Fatal(err)
	}
	out := m.ok(t, "GET", "/api/pricing", "", 200)
	card := out["claude"].(map[string]any)["m"].(map[string]any)
	if card["cache_creation"] != float64(5) || card["input"] != float64(1) {
		t.Fatalf("the stored card did not read: %v", card)
	}

	m.ok(t, "PATCH", "/api/pricing", `{"claude":{"m":{"input":3,"output":4,"cache_creation":6}}}`, 200)
	raw, _, err := m.st.Store.KV().Get(ctx, repository.ScopePricing, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if gjson.Get(raw, `m.cache_creation`).Float() != 6 {
		t.Fatalf("the write did not keep the stored spelling: %s", raw)
	}
	if strings.Contains(raw, "cacheCreation") {
		t.Fatalf("the write used the camel spelling: %s", raw)
	}
	if *reloads == 0 {
		t.Fatal("a write did not reload the table")
	}
	out = m.ok(t, "GET", "/api/pricing", "", 200)
	if got := out["claude"].(map[string]any)["m"].(map[string]any)["input"]; got != float64(3) {
		t.Fatalf("the merged view is stale: %v", got)
	}
}

func TestAPricingWritePricesTheNextLookupAndADeleteRestoresTheEmbeddedOne(t *testing.T) {
	m, c, _ := newPricingRig(t)
	m.ok(t, "PATCH", "/api/pricing", `{"openai":{"gpt-4o":{"input":50,"output":60}}}`, 200)
	tb, err := c.Table()
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := tb.Lookup("openai", "gpt-4o"); !ok || r.Input != 50 {
		t.Fatalf("%+v %v", r, ok)
	}

	m.ok(t, "DELETE", "/api/pricing?provider=openai&model=gpt-4o", "", 200)
	raw, ok, err := m.st.Store.KV().Get(context.Background(), repository.ScopePricing, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if ok && gjson.Get(raw, "gpt-4o").Exists() {
		t.Fatalf("the model stayed in the row: %s", raw)
	}
	base, err := upstream.DefaultTable(m.pc.Registry)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := base.Lookup("openai", "gpt-4o")
	tb, _ = c.Table()
	if got, _ := tb.Lookup("openai", "gpt-4o"); got != want {
		t.Fatalf("the embedded rate did not come back: %+v vs %+v", got, want)
	}

	m.ok(t, "DELETE", "/api/pricing", "", 200)
	if rows, err := m.st.Store.KV().List(context.Background(), repository.ScopePricing); err != nil || len(rows) != 0 {
		t.Fatalf("the scope was not cleared: %v %v", rows, err)
	}
}

func TestAPricingWriteRefusesABadCardAndWritesNothing(t *testing.T) {
	m, _, reloads := newPricingRig(t)
	for name, body := range map[string]struct{ body, want string }{
		"negative":      {`{"p":{"m":{"input":-1}}}`, "Invalid pricing value for input in p/m: must be non-negative number"},
		"unknown field": {`{"p":{"m":{"inptu":1}}}`, "Invalid pricing field: inptu for p/m"},
		"not a number":  {`{"p":{"m":{"input":"5"}}}`, "Invalid pricing value for input in p/m: must be non-negative number"},
		"not an object": {`{"p":[]}`, "Invalid pricing for provider: p"},
		"that is null":  {`{"p":null}`, "Invalid pricing for provider: p"},
		"empty":         {`{}`, "Invalid pricing data format"},
		"not json":      {`[`, "Invalid pricing data format"},
	} {
		code, out, raw := m.call(nethttp.MethodPatch, "/api/pricing", body.body, true)
		if code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
			continue
		}
		if msg, _ := out["error"].(string); msg != body.want {
			t.Errorf("%s: %q", name, msg)
		}
	}
	if rows, err := m.st.Store.KV().List(context.Background(), repository.ScopePricing); err != nil || len(rows) != 0 {
		t.Fatalf("a refused write stored something: %v %v", rows, err)
	}
	if *reloads != 0 {
		t.Fatal("a refused write reloaded the table")
	}
}

func TestACorruptStoredRowIsNotOverwrittenByAWrite(t *testing.T) {
	m, _, _ := newPricingRig(t)
	ctx := context.Background()
	for _, bad := range []string{`["not","an","object"]`, `true`, `"text"`, `12`} {
		if err := m.st.Store.KV().Set(ctx, repository.ScopePricing, "openai", bad); err != nil {
			t.Fatal(err)
		}
		m.ok(t, "PATCH", "/api/pricing", `{"openai":{"gpt-4o":{"input":1,"output":2}}}`, 500)
		if raw, _, _ := m.st.Store.KV().Get(ctx, repository.ScopePricing, "openai"); raw != bad {
			t.Fatalf("the unreadable row %s was replaced: %s", bad, raw)
		}
	}
}

func TestAWriteKeepsTheOtherModelsOfTheRowAsStored(t *testing.T) {
	m, _, _ := newPricingRig(t)
	ctx := context.Background()
	if err := m.st.Store.KV().Set(ctx, repository.ScopePricing, "openai", `{"keep.me":{"input":7,"output":8},"gpt-4o":{"input":1,"output":2}}`); err != nil {
		t.Fatal(err)
	}
	m.ok(t, "PATCH", "/api/pricing", `{"openai":{"gpt-4o":{"input":50,"output":60},"gpt-4.1.mini":{"input":3,"output":4}}}`, 200)
	raw, _, _ := m.st.Store.KV().Get(ctx, repository.ScopePricing, "openai")
	if gjson.Get(raw, `keep\.me.input`).Float() != 7 || gjson.Get(raw, "gpt-4o.input").Float() != 50 {
		t.Fatalf("%s", raw)
	}
	if _, ok := gjson.Parse(raw).Map()["gpt-4.1.mini"]; !ok {
		t.Fatalf("a dotted model id was split into a path: %s", raw)
	}
	m.ok(t, "DELETE", "/api/pricing?provider=openai&model=gpt-4o", "", 200)
	raw, _, _ = m.st.Store.KV().Get(ctx, repository.ScopePricing, "openai")
	if gjson.Get(raw, "gpt-4o").Exists() || gjson.Get(raw, `keep\.me.input`).Float() != 7 {
		t.Fatalf("%s", raw)
	}
	m.ok(t, "DELETE", "/api/pricing?provider=openai&model=keep.me", "", 200)
	m.ok(t, "DELETE", "/api/pricing?provider=openai&model=gpt-4.1.mini", "", 200)
	if _, found, _ := m.st.Store.KV().Get(ctx, repository.ScopePricing, "openai"); found {
		t.Fatal("an emptied row stayed")
	}
}
