package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("oauth", map[string]any{
		// Client ids of the command line tools whose logins the router uses.
		// Empty switches that provider's login off.
		"claude_client_id": config.Env("OAUTH_CLAUDE_CLIENT_ID", ""),
		"codex_client_id":  config.Env("OAUTH_CODEX_CLIENT_ID", ""),
		// Address the fixed-port login listener binds to.
		"callback_host": config.Env("OAUTH_CALLBACK_HOST", "127.0.0.1"),
		// Renew access tokens in the background, before a request needs them.
		"background_refresh": config.Env("OAUTH_BACKGROUND_REFRESH", true),
		// Pause between two accounts in a background pass.
		"refresh_delay": config.Env("OAUTH_REFRESH_DELAY", "1500ms"),
	})
}
