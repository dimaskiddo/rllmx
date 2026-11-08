package providers

import (
	"context"
	"testing"

	"github.com/goravel/framework/contracts/foundation"

	"rllmx/app/cache"
	"rllmx/tests"
	"rllmx/tests/store"
)

func responseApp(t *testing.T, redisHost string, bound bool) *fakeApp {
	t.Helper()
	app := &fakeApp{cfg: tests.Config{Values: map[string]any{
		"database.redis.default.host": redisHost,
		"cache.response.max_entries":  7,
		"cache.response.max_mb":       3,
	}}, log: &tests.Log{}}
	if bound {
		app.Singleton(BindingCache, func(_ foundation.Application) (any, error) {
			return cache.New(tests.NewCache(), cache.WithoutShared()), nil
		})
	}
	return app
}

func TestTheResponseCacheIsBoundedInProcessAndNotWhenRedisIsConfigured(t *testing.T) {
	st := store.Migrated(t)
	rc := responseCache(responseApp(t, "", true), st.Store)
	if rc == nil || rc.Budget == nil || rc.Budget.MaxEntries != 7 || rc.Budget.MaxBytes != 3<<20 || rc.Flags == nil {
		t.Fatalf("%+v", rc)
	}
	shared := responseCache(responseApp(t, "127.0.0.1", true), st.Store)
	if shared == nil || shared.Budget != nil {
		t.Fatalf("a shared store must carry no process budget: %+v", shared)
	}
}

func TestNoBoundCacheLeavesTheResponseCacheOff(t *testing.T) {
	if rc := responseCache(responseApp(t, "", false), store.Migrated(t).Store); rc != nil {
		t.Fatalf("%+v", rc)
	}
}

func TestASettingsWritePurgesTheSettingsProjectionAndTheAnswers(t *testing.T) {
	c := cache.New(tests.NewCache(), cache.WithoutShared())
	c.Set(cache.Settings, "response", []byte("x"), 1e12)
	c.Set(cache.Response, "k", []byte("y"), 1e12)
	c.Set(cache.APIKeys, "rc:1", []byte("z"), 1e12)
	purgeResponseCache(c)
	if _, ok := c.Get(cache.Settings, "response"); ok {
		t.Error("the settings projection survived")
	}
	if _, ok := c.Get(cache.Response, "k"); ok {
		t.Error("an answer survived")
	}
	if _, ok := c.Get(cache.APIKeys, "rc:1"); !ok {
		t.Error("an unrelated group was purged")
	}
	purgeResponseCache(nil)
}

func TestAWriteToTheSettingsRowPurgesTheCacheThroughTheStore(t *testing.T) {
	st := store.Migrated(t)
	c := cache.New(tests.NewCache(), cache.WithoutShared())
	onSettingsWrite(st.Store, func() *cache.Cache { return c })
	c.Set(cache.Settings, "response", []byte("x"), 1e12)
	c.Set(cache.Response, "k", []byte("y"), 1e12)
	c.Set(cache.Discovery, "d", []byte("z"), 1e12)
	if err := st.Settings().Patch(context.Background(), map[string]string{"responseCache": `{"enabled":true}`}); err != nil {
		t.Fatal(err)
	}
	for g, key := range map[cache.Group]string{cache.Settings: "response", cache.Response: "k", cache.Discovery: "d"} {
		if _, ok := c.Get(g, key); ok {
			t.Errorf("%s survived a settings write", g)
		}
	}
}
