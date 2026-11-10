package providers

import (
	"context"
	"sync/atomic"
	"testing"

	"rllmx/app/store/document"
	"rllmx/tests/store"
)

type countingSettings struct {
	reads atomic.Int32
	inner settingsReader
}

func (c *countingSettings) Effective(ctx context.Context) (document.Doc, error) {
	c.reads.Add(1)
	return c.inner.Effective(ctx)
}

func TestSettingsAreReadOnceForManyRequestsAndAWriteReachesTheNext(t *testing.T) {
	st := store.Migrated(t)
	src := &countingSettings{inner: st.Settings()}
	cached := newCachedSettings(src)
	st.OnSettingsChange(cached.Drop)
	ctx := context.Background()

	for range 20 {
		if _, err := cached.Effective(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := src.reads.Load(); n != 1 {
		t.Fatalf("%d reads for 20 requests", n)
	}

	if err := st.Settings().Patch(ctx, map[string]string{"providerOverrides": `{"openai":{"headers":{"X-Test":"1"}}}`}); err != nil {
		t.Fatal(err)
	}
	doc, err := cached.Effective(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if raw, ok := doc.GetRaw("providerOverrides"); !ok || raw != `{"openai":{"headers":{"X-Test":"1"}}}` {
		t.Fatalf("the next request did not see the write: %q %v", raw, ok)
	}
	if n := src.reads.Load(); n != 2 {
		t.Fatalf("%d reads after a write", n)
	}
}

func mustRaw(d document.Doc) string {
	raw, _ := d.GetRaw("providerOverrides")
	return raw
}

func TestTheStoreDropsItsSettingsCacheOnAWrite(t *testing.T) {
	st := store.Migrated(t)
	ctx := context.Background()
	c := settingsFor(st.Store)
	if doc, _ := c.Effective(ctx); mustRaw(doc) != "{}" {
		t.Fatalf("a fresh store has overrides: %s", mustRaw(doc))
	}
	if err := st.Settings().Patch(ctx, map[string]string{"providerOverrides": `{"a":{"headers":{"H":"v"}}}`}); err != nil {
		t.Fatal(err)
	}
	if doc, _ := c.Effective(ctx); mustRaw(doc) != `{"a":{"headers":{"H":"v"}}}` {
		t.Fatalf("a write through the store did not reach the next read: %s", mustRaw(doc))
	}
}

func TestHeaderOverridesComeFromTheCachedSettings(t *testing.T) {
	st := store.Migrated(t)
	ctx := context.Background()
	if err := st.Settings().Patch(ctx, map[string]string{"providerOverrides": `{"openai":{"headers":{"X-Test":"1"}}}`}); err != nil {
		t.Fatal(err)
	}
	src := &countingSettings{inner: st.Settings()}
	o := headerOverrides{settings: newCachedSettings(src)}
	for range 5 {
		if got := o.Headers(ctx, "openai"); got["X-Test"] != "1" {
			t.Fatalf("headers %v", got)
		}
	}
	if n := src.reads.Load(); n != 1 {
		t.Fatalf("%d reads for 5 calls", n)
	}
}
