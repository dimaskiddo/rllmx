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
		// The TLS key-exchange group an outbound call offers: x25519 (default) or
		// default for the standard list. One group changes the ClientHello enough
		// for a network that resets the standard offer.
		"tls_curve": config.Env("UPSTREAM_TLS_CURVE", "x25519"),
		// The claude-cli/x.y.z the Claude fingerprint advertises. Empty reads the
		// latest published @anthropic-ai/claude-code from npm every six hours; a
		// value pins it and turns the lookup off.
		"claude_cli_version": config.Env("UPSTREAM_CLAUDE_CLI_VERSION", ""),
	})
}
