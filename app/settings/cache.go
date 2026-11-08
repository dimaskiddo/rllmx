package settings

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/goravel/framework/contracts/config"

	"rllmx/app/network"
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
	// Shared is true when a Redis (or Valkey) host is configured.
	Shared bool
	// ResponseMaxEntries and ResponseMaxBytes bound the response cache in the
	// process memory store. They do not apply when Shared is true.
	ResponseMaxEntries int
	ResponseMaxBytes   int64
	// Warnings are start-up notices. They never repeat a configured value.
	Warnings []string
}

// LoadCache reads the cache settings. The REDIS_* values are not checked while
// REDIS_HOST is empty, because nothing reads them then.
func LoadCache(cfg config.Config) (*Cache, error) {
	entries := cfg.GetInt(responseEntriesKey, 1000)
	if entries < 1 || entries > maxResponseEntries {
		return nil, fmt.Errorf("settings: CACHE_RESPONSE_MAX_ENTRIES: must be between 1 and %d", maxResponseEntries)
	}
	mb := cfg.GetInt(responseMBKey, 64)
	if mb < 1 || mb > maxResponseMB {
		return nil, fmt.Errorf("settings: CACHE_RESPONSE_MAX_MB: must be between 1 and %d", maxResponseMB)
	}
	limits := Cache{ResponseMaxEntries: entries, ResponseMaxBytes: int64(mb) << 20}
	host := cfg.GetString("database.redis.default.host")
	if host == "" {
		return &limits, nil
	}
	if p, err := strconv.Atoi(cfg.GetString("database.redis.default.port", "6379")); err != nil || p < 1 || p > 65535 {
		return nil, errors.New("settings: REDIS_PORT: must be between 1 and 65535")
	}
	if cfg.GetInt("database.redis.default.database") < 0 {
		return nil, errors.New("settings: REDIS_DB: must not be negative")
	}
	c := &limits
	c.Shared = true
	if !network.IsLoopbackHost(host) {
		c.Warnings = append(c.Warnings, "REDIS_HOST is not this machine and the connection is not encrypted: cached values and the Redis password cross the network in clear text")
	}
	return c, nil
}
