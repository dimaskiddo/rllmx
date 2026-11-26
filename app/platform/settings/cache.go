package settings

import (
	"errors"
	"strconv"

	"github.com/goravel/framework/contracts/config"

	"rllmx/app/platform/network"
)

const (
	responseEntriesKey = "cache.response.max_entries"
	responseMBKey      = "cache.response.max_mb"

	maxResponseEntries = 100000
	maxResponseMB      = 4096
)

// Cache is the validated view of the cache settings: whether Redis is used, and
// how much the in-process response cache may hold. Redis is optional: with no
// host the shared groups stay in the process memory store.
type Cache struct {
	Notices

	// Shared is true when a Redis (or Valkey) host is configured.
	Shared bool
	// ResponseMaxEntries and ResponseMaxBytes bound the response cache in the
	// process memory store. They do not apply when Shared is true.
	ResponseMaxEntries int
	ResponseMaxBytes   int64
}

// LoadCache reads the cache settings. The sizes are tuning values and fall back
// to their defaults; the REDIS_* endpoint is not checked while REDIS_HOST is
// empty, because nothing reads it then.
func LoadCache(cfg config.Config) (*Cache, error) {
	c := &Cache{}
	c.ResponseMaxEntries = number(cfg, responseEntriesKey, "CACHE_RESPONSE_MAX_ENTRIES", 1, maxResponseEntries, 1000, &c.Notices)
	c.ResponseMaxBytes = int64(number(cfg, responseMBKey, "CACHE_RESPONSE_MAX_MB", 1, maxResponseMB, 64, &c.Notices)) << 20
	host := cfg.GetString("database.redis.default.host")
	if host == "" {
		return c, nil
	}
	if p, err := strconv.Atoi(cfg.GetString("database.redis.default.port", "6379")); err != nil || p < 1 || p > 65535 {
		return nil, errors.New("settings: REDIS_PORT: must be between 1 and 65535")
	}
	if cfg.GetInt("database.redis.default.database") < 0 {
		return nil, errors.New("settings: REDIS_DB: must not be negative")
	}
	c.Shared = true
	if !network.IsLoopbackHost(host) {
		c.Warnings = append(c.Warnings, "REDIS_HOST is not this machine and the connection is not encrypted: cached values and the Redis password cross the network in clear text")
	}
	return c, nil
}
