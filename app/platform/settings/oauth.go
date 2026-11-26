package settings

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
	"unicode"

	"github.com/goravel/framework/contracts/config"
)

const (
	maxClientIDLen = 256
	maxRefreshGap  = time.Minute
)

// oauthClients maps a provider id to the config key holding its client id.
var oauthClients = map[string]struct{ key, env string }{
	"claude": {"oauth.claude_client_id", "OAUTH_CLIENT_ID_CLAUDE"},
	"codex":  {"oauth.codex_client_id", "OAUTH_CLIENT_ID_CODEX"},
	"github": {"oauth.github_client_id", "OAUTH_CLIENT_ID_GITHUB"},
	"kimi":   {"oauth.kimi_client_id", "OAUTH_CLIENT_ID_KIMI"},
	"muse":   {"oauth.muse_client_id", "OAUTH_CLIENT_ID_MUSE"},
	"nous":   {"oauth.nous_client_id", "OAUTH_CLIENT_ID_NOUS"},
	"xai":    {"oauth.xai_client_id", "OAUTH_CLIENT_ID_XAI"},
	// Both MiniMax Code sites log in with this one id.
	"minimax-code": {"oauth.minimax_code_client_id", "OAUTH_CLIENT_ID_MINIMAX_CODE"},
	// Gemini CLI is the Google client; Antigravity has its own.
	"gemini-cli":  {"oauth.google_client_id", "OAUTH_CLIENT_ID_GOOGLE"},
	"antigravity": {"oauth.antigravity_client_id", "OAUTH_CLIENT_ID_ANTIGRAVITY"},
}

// oauthSecrets maps a provider id to the config key holding its client secret.
// Only a client that has one is listed.
var oauthSecrets = map[string]struct{ key, env string }{
	"gemini-cli":  {"oauth.google_client_secret", "OAUTH_CLIENT_SECRET_GOOGLE"},
	"antigravity": {"oauth.antigravity_client_secret", "OAUTH_CLIENT_SECRET_ANTIGRAVITY"},
}

// sharedClients lists the providers that log in with another provider's client
// id: Grok CLI is the xAI client, the global MiniMax Code site the China one.
var sharedClients = map[string]string{"grok-cli": "xai", "minimax-code-global": "minimax-code"}

// OAuth is the validated view of the OAUTH_* settings.
type OAuth struct {
	Notices

	// CallbackHost is the address the fixed-port login listener binds to.
	CallbackHost string
	// BackgroundRefresh turns the periodic refresh pass on.
	BackgroundRefresh bool
	// RefreshDelay is the pause between two accounts in that pass.
	RefreshDelay time.Duration
	// RefreshGoogleDelay is the longer pause after an account of a provider that
	// is slow to renew (Google).
	RefreshGoogleDelay time.Duration
	clients            map[string]string
	secrets            map[string]string
}

// LoadOAuth reads and checks the OAUTH_* settings. An empty client id is valid:
// it switches that provider's login off.
func LoadOAuth(cfg config.Config) (*OAuth, error) {
	o := &OAuth{clients: map[string]string{}, secrets: map[string]string{}}
	host := strings.TrimSpace(cfg.GetString("oauth.callback_host"))
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Zone() != "" {
		return nil, errors.New("settings: OAUTH_CALLBACK_HOST: must be an IP address")
	}
	o.CallbackHost = addr.String()
	o.BackgroundRefresh = cfg.GetBool("oauth.background_refresh")
	o.RefreshDelay = duration(cfg, "oauth.refresh_delay", "OAUTH_REFRESH_DELAY", 0, maxRefreshGap, 1500*time.Millisecond, &o.Notices)
	o.RefreshGoogleDelay = duration(cfg, "oauth.refresh_google_delay", "OAUTH_REFRESH_GOOGLE_DELAY", 0, maxRefreshGap, 12*time.Second, &o.Notices)
	for id, c := range oauthSecrets {
		v := strings.TrimSpace(cfg.GetString(c.key))
		if len(v) > maxClientIDLen || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, fmt.Errorf("settings: %s: must be at most %d characters without spaces", c.env, maxClientIDLen)
		}
		if v != "" {
			o.secrets[id] = v
		}
	}
	for id, c := range oauthClients {
		v := strings.TrimSpace(cfg.GetString(c.key))
		if len(v) > maxClientIDLen || strings.ContainsFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, fmt.Errorf("settings: %s: must be at most %d characters without spaces", c.env, maxClientIDLen)
		}
		if v != "" {
			o.clients[id] = v
		}
	}
	return o, nil
}

// ClientID returns the provider's OAuth client id, or "" when its login is off.
func (o *OAuth) ClientID(provider string) string {
	if o == nil {
		return ""
	}
	if owner, shared := sharedClients[provider]; shared {
		provider = owner
	}
	return o.clients[provider]
}

// ClientSecret returns the provider's OAuth client secret, or "" when its client
// has none. The value is never logged or returned by a route.
func (o *OAuth) ClientSecret(provider string) string {
	if o == nil {
		return ""
	}
	if owner, shared := sharedClients[provider]; shared {
		provider = owner
	}
	return o.secrets[provider]
}
