package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("upstream", map[string]any{
		// How long one outbound connection may take to establish, and how long a
		// response may take to start arriving.
		"connect_timeout": config.Env("UPSTREAM_CONNECT_TIMEOUT", "60s"),
		// Addresses and names an outbound call may reach when they are not
		// public: loopback, the private ranges, carrier-grade NAT. Link-local and
		// the cloud metadata addresses are refused whatever this says.
		"allow_private": config.Env("UPSTREAM_ALLOW_PRIVATE", ""),
	})
}
