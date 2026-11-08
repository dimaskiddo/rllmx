package config

import (
	"github.com/goravel/framework/contracts/route"
	ginfacades "github.com/goravel/gin/facades"

	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("http", map[string]any{
		"default": "gin",
		// HTTP Drivers
		"drivers": map[string]any{
			"gin": map[string]any{
				// KiB. body_limit sizes the multipart memory only; the request
				// body is bounded by our own middleware.
				"body_limit":   4096,
				"header_limit": 64,
				"route": func() (route.Route, error) {
					return ginfacades.Route("gin"), nil
				},
			},
		},
		// Listener
		//
		// The framework's own server has no header or idle timeout and no key sets
		// one, so a provider runner owns the http.Server and reads these.
		// shutdown_grace is how long a shutdown lets running requests finish.
		"server": map[string]any{
			"read_header_timeout": config.Env("HTTP_SERVER_READ_HEADER_TIMEOUT", "10s"),
			"idle_timeout":        config.Env("HTTP_SERVER_IDLE_TIMEOUT", "120s"),
			"shutdown_grace":      config.Env("HTTP_SERVER_SHUTDOWN_GRACE", "30s"),
		},
		// Request guard
		//
		// max_body_mb bounds a client API body, trust_loopback lets a request from
		// this machine count as local, and trusted_proxies lists the addresses or
		// CIDR ranges whose X-Forwarded-For and X-Forwarded-Proto are believed.
		"guard": map[string]any{
			"max_body_mb":     config.Env("SERVER_MAX_BODY_MB", 128),
			"trust_loopback":  config.Env("SERVER_TRUST_LOOPBACK", true),
			"trusted_proxies": config.Env("SERVER_TRUSTED_PROXIES", ""),
		},
		// HTTP URL
		"url": config.Env("APP_URL", ""),
		// HTTP Host
		"host": config.Env("APP_HOST", "127.0.0.1"),
		// HTTP Port
		"port": config.Env("APP_PORT", "20128"),
		// Seconds. 0 turns the framework timeout middleware off: it buffers the
		// response, which would cut a stream that runs for minutes.
		"request_timeout": 0,
		// HTTPS Configuration
		"tls": map[string]any{
			// HTTPS Host
			"host": config.Env("APP_HOST", "127.0.0.1"),
			// HTTPS Port
			"port": config.Env("APP_PORT", "20128"),
			// SSL Certificate, you can put the certificate in /public folder
			"ssl": map[string]any{
				// ca.pem
				"cert": "",
				// ca.key
				"key": "",
			},
		},
		// Default Client Name
		//
		// This determines which client is used when you call facades.Http() or
		// facades.Http().Client() without passing a specific name.
		"default_client": "default",
		// Client Configurations
		//
		// Here you may define multiple independent client configurations.
		// For example, you might have a "github" client with a specific base URL
		// and a "stripe" client with a longer timeout.
		"clients": map[string]any{
			"default": map[string]any{
				// The base URL for the client. All requests made using this client
				// will be relative to this URL.
				"base_url": "",
				// The maximum amount of time a request can take, including connection
				// establishment, redirects, and reading the response body.
				"timeout": "30s",
				// The maximum number of idle (keep-alive) connections to keep across
				// ALL hosts. Increasing this helps reuse TCP connections.
				"max_idle_conns": 100,
				// The maximum number of idle (keep-alive) connections to keep PER host.
				// By default, Go sets this to 2, which is often a bottleneck.
				// Increase this value for high-throughput applications.
				"max_idle_conns_per_host": 2,
				// The maximum total number of connections (active + idle) allowed per host.
				// A value of 0 means no limit.
				"max_conns_per_host": 0,
				// The maximum amount of time an idle (keep-alive) connection will remain
				// in the pool before closing itself.
				"idle_conn_timeout": "90s",
			},
		},
	})
}
