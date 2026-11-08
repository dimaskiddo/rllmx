package settings

import (
	"maps"
	"strings"
	"testing"

	"rllmx/tests"
)

func cacheValues(over map[string]any) tests.Config {
	m := map[string]any{
		"database.redis.default.host":     "",
		"database.redis.default.port":     "6379",
		"database.redis.default.password": "",
		"database.redis.default.database": 0,
	}
	maps.Copy(m, over)
	return tests.Config{Values: m}
}

func TestEmptyRedisHostMeansMemoryOnly(t *testing.T) {
	c, err := LoadCache(cacheValues(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Shared || len(c.Warnings) != 0 {
		t.Fatalf("%+v", c)
	}
}

func TestARedisHostTurnsTheSharedStoreOn(t *testing.T) {
	c, err := LoadCache(cacheValues(map[string]any{"database.redis.default.host": "127.0.0.1"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Shared || len(c.Warnings) != 0 {
		t.Fatalf("a loopback Redis needs no warning: %+v", c)
	}
}

func TestPlainRedisToAnotherHostWarnsWithoutNamingThePassword(t *testing.T) {
	c, err := LoadCache(cacheValues(map[string]any{
		"database.redis.default.host":     "cache.example.com",
		"database.redis.default.password": "pw-secret-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Shared || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "REDIS_HOST") {
		t.Fatalf("%+v", c)
	}
	if strings.Contains(c.Warnings[0], "pw-secret-1") || strings.Contains(c.Warnings[0], "cache.example.com") {
		t.Fatalf("the warning repeats a configured value: %q", c.Warnings[0])
	}
}

func TestRedisPortAndDatabaseBounds(t *testing.T) {
	with := func(k string, v any) tests.Config {
		return cacheValues(map[string]any{k: v, "database.redis.default.host": "localhost"})
	}
	for _, v := range []string{"1", "6379", "65535"} {
		if _, err := LoadCache(with("database.redis.default.port", v)); err != nil {
			t.Errorf("port %s rejected: %v", v, err)
		}
	}
	for _, v := range []string{"0", "65536", "-1", "redis"} {
		if _, err := LoadCache(with("database.redis.default.port", v)); err == nil || !strings.Contains(err.Error(), "REDIS_PORT") {
			t.Errorf("port %s: error = %v", v, err)
		}
	}
	for _, v := range []int{0, 15, 1000} {
		if _, err := LoadCache(with("database.redis.default.database", v)); err != nil {
			t.Errorf("db %d rejected: %v", v, err)
		}
	}
	if _, err := LoadCache(with("database.redis.default.database", -1)); err == nil || !strings.Contains(err.Error(), "REDIS_DB") {
		t.Errorf("db -1: error = %v", err)
	}
}

func TestRedisValuesAreNotCheckedWhileRedisIsOff(t *testing.T) {
	if _, err := LoadCache(cacheValues(map[string]any{"database.redis.default.port": "junk"})); err != nil {
		t.Fatalf("a port nobody uses must not stop the start: %v", err)
	}
}

func TestResponseCacheLimitsHaveDefaultsAndRanges(t *testing.T) {
	c, err := LoadCache(cacheValues(nil))
	if err != nil || c.ResponseMaxEntries != 1000 || c.ResponseMaxBytes != 64<<20 {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = LoadCache(cacheValues(map[string]any{"cache.response.max_entries": 5, "cache.response.max_mb": 2}))
	if err != nil || c.ResponseMaxEntries != 5 || c.ResponseMaxBytes != 2<<20 {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = LoadCache(cacheValues(map[string]any{"database.redis.default.host": "127.0.0.1", "cache.response.max_mb": 9}))
	if err != nil || !c.Shared || c.ResponseMaxBytes != 9<<20 {
		t.Fatalf("the limits must survive a Redis host: %+v %v", c, err)
	}
	for key, name := range map[string]string{"cache.response.max_entries": "CACHE_RESPONSE_MAX_ENTRIES", "cache.response.max_mb": "CACHE_RESPONSE_MAX_MB"} {
		for _, bad := range []int{0, -1, 100001 + 5000} {
			if _, err := LoadCache(cacheValues(map[string]any{key: bad})); err == nil || !strings.Contains(err.Error(), name) {
				t.Errorf("%s=%d: %v", name, bad, err)
			}
		}
	}
}
