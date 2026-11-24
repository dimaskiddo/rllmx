package tests

import (
	"time"

	"github.com/goravel/framework/contracts/config"
	"github.com/goravel/framework/support/convert"
	"github.com/spf13/cast"
)

// Config is a config.Config backed by a map, for tests that need a few keys.
// A key that is not in Values answers its default. Any other method panics.
type Config struct {
	config.Config
	Values map[string]any
}

func (c Config) get(path string, def any) any {
	if v, ok := c.Values[path]; ok {
		return v
	}
	return def
}

func (c Config) Get(path string, d ...any) any { return c.get(path, convert.Default(d...)) }

func (c Config) GetString(path string, d ...string) string {
	return cast.ToString(c.get(path, convert.Default(d...)))
}

func (c Config) GetInt(path string, d ...int) int {
	return cast.ToInt(c.get(path, convert.Default(d...)))
}

func (c Config) GetBool(path string, d ...bool) bool {
	return cast.ToBool(c.get(path, convert.Default(d...)))
}

func (c Config) GetDuration(path string, d ...time.Duration) time.Duration {
	return cast.ToDuration(c.get(path, convert.Default(d...)))
}

// ServerValues are the listener and guard settings at their defaults.
func ServerValues() map[string]any {
	return map[string]any{
		"http.host":                       "127.0.0.1",
		"http.port":                       "20128",
		"http.url":                        "",
		"http.server.read_header_timeout": "10s",
		"http.server.idle_timeout":        "120s",
		"http.server.shutdown_grace":      "30s",
		"auth.session_ttl":                "24h",
		"http.compression":                true,
		"http.h2c":                        false,
		"http.drivers.gin.header_limit":   64,
		"http.tls.ssl.cert":               "",
		"http.tls.ssl.key":                "",
		"http.guard.max_body_mb":          128,
		"http.guard.trust_loopback":       true,
		"http.guard.trusted_proxies":      "",
		"upstream.connect_timeout":        "60s",
		"upstream.allow_private":          "",
		"upstream.claude_cli_version":     "",
		"stream.first_chunk_timeout":      "200s",
		"stream.stall_timeout":            "360s",
		"stream.usage_wait":               "3s",
		"media.video_timeout":             "120s",
		"media.searxng_url":               "",
		"oauth.callback_host":             "127.0.0.1",
		"oauth.background_refresh":        true,
		"oauth.refresh_delay":             "1500ms",
		"app.data_dir":                    "/var/lib/rllmx",
		"database.redis.default.host":     "",
		"database.redis.default.port":     "6379",
		"database.redis.default.database": 0,
	}
}
