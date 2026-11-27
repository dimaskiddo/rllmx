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
		// Outbound pacing per connection: how many calls a minute one connection
		// may make, how many may leave at once, and how long a call may queue
		// before it is refused instead. Zero turns pacing off.
		"rate_limit_rpm":      config.Env("UPSTREAM_RATE_LIMIT_RPM", 60),
		"rate_limit_burst":    config.Env("UPSTREAM_RATE_LIMIT_BURST", 10),
		"rate_limit_max_wait": config.Env("UPSTREAM_RATE_LIMIT_MAX_WAIT", "30s"),
		// The claude-cli/x.y.z the Claude fingerprint advertises. Empty reads the
		// latest published @anthropic-ai/claude-code from npm every six hours; a
		// value pins it and turns the lookup off.
		"claude_cli_version": config.Env("UPSTREAM_CLAUDE_CLI_VERSION", ""),
		// When a Kiro request uses the Responses transport: auto falls back to it
		// after the rate-limit answer the IDE transport gives enrolled accounts,
		// always uses it for every request, off never does.
		"kiro_create_response": config.Env("UPSTREAM_KIRO_CREATE_RESPONSE", "auto"),
		// The key MiniMax Code signs its account calls with. It is configuration,
		// never a literal in the code; empty leaves that reading unavailable.
		"minimax_sign_secret": config.Env("UPSTREAM_MINIMAX_SIGN_SECRET", ""),
	})
}
