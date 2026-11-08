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
	"claude": {"oauth.claude_client_id", "OAUTH_CLAUDE_CLIENT_ID"},
	"codex":  {"oauth.codex_client_id", "OAUTH_CODEX_CLIENT_ID"},
}

// OAuth is the validated view of the OAUTH_* settings.
type OAuth struct {
	// CallbackHost is the address the fixed-port login listener binds to.
	CallbackHost string
	// BackgroundRefresh turns the periodic refresh pass on.
	BackgroundRefresh bool
	// RefreshDelay is the pause between two accounts in that pass.
	RefreshDelay time.Duration
	clients      map[string]string
}

// LoadOAuth reads and checks the OAUTH_* settings. An empty client id is valid:
// it switches that provider's login off.
func LoadOAuth(cfg config.Config) (*OAuth, error) {
	o := &OAuth{clients: map[string]string{}}
	host := strings.TrimSpace(cfg.GetString("oauth.callback_host"))
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Zone() != "" {
		return nil, errors.New("settings: OAUTH_CALLBACK_HOST: must be an IP address")
	}
	o.CallbackHost = addr.String()
	o.BackgroundRefresh = cfg.GetBool("oauth.background_refresh")
	o.RefreshDelay = cfg.GetDuration("oauth.refresh_delay")
	if o.RefreshDelay < 0 || o.RefreshDelay > maxRefreshGap {
		return nil, fmt.Errorf("settings: OAUTH_REFRESH_DELAY: must be a duration between 0s and %s", maxRefreshGap)
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
	return o.clients[provider]
}
