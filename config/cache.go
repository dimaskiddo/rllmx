package config

import (
	"github.com/goravel/framework/contracts/cache"
	redisfacades "github.com/goravel/redis/facades"

	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("cache", map[string]any{
		// The memory store is the default. Redis or Valkey is used by name
		// (Cache().Store("redis")) when REDIS_HOST is set.
		"default": "memory",

		// Cache Stores
		//
		// Here you may define all the cache "stores" for your application as
		// well as their drivers. You may even define multiple stores for the
		// same cache driver to group types of items stored in your caches.
		// Available Drivers: "memory", "custom"
		"stores": map[string]any{
			"memory": map[string]any{
				"driver": "memory",
			},
			"redis": map[string]any{
				"driver":     "custom",
				"connection": "default",
				"via": func() (cache.Driver, error) {
					return redisfacades.Cache("redis") // The `redis` value is the key of `stores`
				},
			},
		},

		// Cache Key Prefix
		//
		// When utilizing a RAM based store such as APC or Memcached, there might
		// be other applications utilizing the same cache. So, we'll specify a
		// value to get prefixed to all our keys, so we can avoid collisions.
		// Must: a-zA-Z0-9_-
		"prefix": "rllmx_cache",

		// The in-process response cache stops storing at either limit. They do
		// not apply when REDIS_HOST is set: the server has its own.
		"response": map[string]any{
			"max_entries": config.Env("CACHE_RESPONSE_MAX_ENTRIES", 1000),
			"max_mb":      config.Env("CACHE_RESPONSE_MAX_MB", 64),
		},
	})
}
