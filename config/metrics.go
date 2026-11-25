package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("metrics", map[string]any{
		// The bearer token GET /metrics accepts. Empty, with no username, serves
		// no endpoint.
		"token": config.Env("METRICS_TOKEN", ""),
		// The HTTP Basic pair the same route accepts. Both or neither.
		"username": config.Env("METRICS_USERNAME", ""),
		"password": config.Env("METRICS_PASSWORD", ""),
	})
}
