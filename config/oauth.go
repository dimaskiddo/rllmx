package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("oauth", map[string]any{
		// Client ids of the command line tools whose logins the router uses.
		// Empty switches that provider's login off.
		"claude_client_id": config.Env("OAUTH_CLIENT_ID_CLAUDE", ""),
		"codex_client_id":  config.Env("OAUTH_CLIENT_ID_CODEX", ""),
		"github_client_id": config.Env("OAUTH_CLIENT_ID_GITHUB", ""),
		"kimi_client_id":   config.Env("OAUTH_CLIENT_ID_KIMI", ""),
		"muse_client_id":   config.Env("OAUTH_CLIENT_ID_MUSE", ""),
		// Both MiniMax Code sites (China and global) log in with this id.
		"minimax_code_client_id": config.Env("OAUTH_CLIENT_ID_MINIMAX_CODE", ""),
		"nous_client_id":         config.Env("OAUTH_CLIENT_ID_NOUS", ""),
		// Grok CLI logs in with this id too.
		"xai_client_id": config.Env("OAUTH_CLIENT_ID_XAI", ""),
		// Gemini CLI logs in with the Google client, Antigravity with its own; both
		// clients have a secret.
		"google_client_id":          config.Env("OAUTH_CLIENT_ID_GOOGLE", ""),
		"google_client_secret":      config.Env("OAUTH_CLIENT_SECRET_GOOGLE", ""),
		"antigravity_client_id":     config.Env("OAUTH_CLIENT_ID_ANTIGRAVITY", ""),
		"antigravity_client_secret": config.Env("OAUTH_CLIENT_SECRET_ANTIGRAVITY", ""),
		// Address the fixed-port login listener binds to.
		"callback_host": config.Env("OAUTH_CALLBACK_HOST", "127.0.0.1"),
		// Renew access tokens in the background, before a request needs them.
		"background_refresh": config.Env("OAUTH_BACKGROUND_REFRESH", true),
		// Pause between two accounts in a background pass.
		"refresh_delay": config.Env("OAUTH_REFRESH_DELAY", "1500ms"),
		// Pause after an account of a provider Google rate-limits renewals for.
		"refresh_google_delay": config.Env("OAUTH_REFRESH_GOOGLE_DELAY", "12s"),
	})
}
