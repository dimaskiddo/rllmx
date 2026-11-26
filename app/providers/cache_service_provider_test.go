package providers

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/goravel/framework/contracts/binding"
	gocache "github.com/goravel/framework/contracts/cache"
	"github.com/goravel/framework/contracts/config"

	"rllmx/app/platform/cache"
	"rllmx/app/platform/settings"
	"rllmx/tests"
)

type cacheApp struct {
	fakeApp
	cache gocache.Cache
}

func (a *cacheApp) MakeCache() gocache.Cache { return a.cache }

func cacheValues(over map[string]any) config.Config {
	m := map[string]any{
		"database.redis.default.host":     "",
		"database.redis.default.port":     "6379",
		"database.redis.default.database": 0,
	}
	maps.Copy(m, over)
	return tests.Config{Values: m}
}

func resolveCache(t *testing.T, cfg config.Config) (*cache.Cache, *tests.Log, *tests.Cache, error) {
	t.Helper()
	log := &tests.Log{}
	facade := tests.NewCache()
	app := &cacheApp{fakeApp: fakeApp{cfg: cfg, log: log}, cache: facade}
	(&CacheServiceProvider{}).Register(app)
	cb := app.bindings[BindingCache]
	if cb == nil {
		t.Fatal("Register bound nothing under " + BindingCache)
	}
	v, err := cb(app)
	if err != nil {
		return nil, log, facade, err
	}
	c, ok := v.(*cache.Cache)
	if !ok {
		t.Fatalf("binding = %T", v)
	}
	return c, log, facade, nil
}

func TestTheCacheProviderDependsOnWhatItReads(t *testing.T) {
	rel := (&CacheServiceProvider{}).Relationship()
	for _, want := range []string{binding.Config, binding.Log} {
		if !slices.Contains(rel.Dependencies, want) {
			t.Errorf("%s is read by the provider but is not a dependency: %v", want, rel.Dependencies)
		}
	}
	if !slices.Equal(rel.Bindings, []string{BindingCache}) {
		t.Fatalf("bindings = %v", rel.Bindings)
	}
}

func TestWithNoRedisTheSharedGroupsStayInMemory(t *testing.T) {
	c, log, facade, err := resolveCache(t, cacheValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	c.Set(cache.Settings, "probe", []byte("v"), time.Minute)
	if len(facade.StoreNamed("redis").Keys("")) != 0 {
		t.Fatal("a shared group reached the Redis store with no host configured")
	}
	if len(facade.StoreNamed("memory").Keys("")) == 0 {
		t.Fatal("nothing reached the memory store")
	}
	if got := len(log.Lines()); got != 0 {
		t.Fatalf("a store was reported missing: %v", log.Lines())
	}
}

func TestWithARedisHostTheSharedStoreIsChosen(t *testing.T) {
	c, _, facade, err := resolveCache(t, cacheValues(map[string]any{"database.redis.default.host": "127.0.0.1"}))
	if err != nil {
		t.Fatal(err)
	}
	c.Set(cache.Settings, "probe", []byte("v"), time.Minute)
	if facade.StoreNamed("redis").Keys("") == nil {
		t.Fatal("a shared group did not reach the Redis store")
	}
	// Credential groups stay in process memory whatever the shared store is.
	c.Set(cache.Connections, "probe", []byte("v"), time.Minute)
	if facade.StoreNamed("memory").Keys("") == nil {
		t.Fatal("a credential group left the memory store")
	}
}

// The warning is the loader's: the configuration gate prints it once at start,
// before any provider binds, and it never repeats a configured value.
func TestANonLoopbackRedisWarnsOnceWithoutAValue(t *testing.T) {
	s, err := settings.LoadCache(cacheValues(map[string]any{
		"database.redis.default.host":     "cache.example.test",
		"database.redis.default.password": "pw-should-not-appear",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], "not this machine") {
		t.Fatalf("warnings = %v", s.Warnings)
	}
	for _, l := range s.Warnings {
		for _, leak := range []string{"cache.example.test", "pw-should-not-appear"} {
			if strings.Contains(l, leak) {
				t.Errorf("the warning repeated %q: %s", leak, l)
			}
		}
	}
}

func TestABadRedisSettingStopsTheBinding(t *testing.T) {
	_, _, _, err := resolveCache(t, cacheValues(map[string]any{
		"database.redis.default.host": "127.0.0.1",
		"database.redis.default.port": "not a port",
	}))
	if err == nil {
		t.Fatal("a bad REDIS_PORT was accepted")
	}
}

func TestTheCacheProviderBootDoesNothing(t *testing.T) {
	(&CacheServiceProvider{}).Boot(&fakeApp{})
}

func TestCacheOfReturnsTheBinding(t *testing.T) {
	want := cache.New(tests.NewCache(), cache.WithoutShared())
	app := &fakeApp{values: map[any]any{BindingCache: want}}
	if got := CacheOf(app); got != want {
		t.Fatal("not the bound value")
	}
}

func TestCacheOfPanicsOnAMissingBinding(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("a missing binding must stop the start loudly")
		}
	}()
	CacheOf(&fakeApp{})
}
