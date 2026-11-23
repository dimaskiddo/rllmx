package settings

import (
	"crypto/tls"
	"fmt"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/config"

	"rllmx/app/platform/network"
	"rllmx/app/platform/ratelimit"
)

const (
	minConnectTimeout = time.Second
	maxConnectTimeout = 10 * time.Minute
)

// Upstream is the validated view of the outbound network settings. The allow
// list comes from the environment on purpose: a dashboard session must not be
// able to widen where this program may connect.
type Upstream struct {
	ConnectTimeout time.Duration
	// Allow is nil when nothing private is reachable.
	Allow *network.Allow
	// Curves is the key-exchange set an outbound call offers; empty is the
	// standard list.
	Curves []tls.CurveID
	// ClaudeCLIVersion pins the claude-cli/x.y.z fingerprint. Empty reads the
	// latest published version from npm and keeps the built-in floor.
	ClaudeCLIVersion string
	// RateLimit paces outbound calls per connection; a zero RPM is off.
	RateLimit ratelimit.Config
}

// LoadUpstream reads and checks the UPSTREAM_* settings. A bad allow-list entry
// stops the start and the error never repeats the entry.
func LoadUpstream(cfg config.Config) (*Upstream, error) {
	u := &Upstream{}
	var err error
	if u.ConnectTimeout, err = duration(cfg, "upstream.connect_timeout", "UPSTREAM_CONNECT_TIMEOUT", minConnectTimeout, maxConnectTimeout); err != nil {
		return nil, err
	}
	if u.Allow, err = network.ParseAllow(cfg.GetString("upstream.allow_private")); err != nil {
		return nil, err
	}
	if u.Curves, err = curveSet(cfg.GetString("upstream.tls_curve")); err != nil {
		return nil, err
	}
	if u.ClaudeCLIVersion, err = cliVersion(cfg.GetString("upstream.claude_cli_version")); err != nil {
		return nil, err
	}
	rate := ratelimit.Config{RPM: cfg.GetInt("upstream.rate_limit_rpm", 60), Burst: cfg.GetInt("upstream.rate_limit_burst", 10)}
	if rate.MaxWait, err = duration(cfg, "upstream.rate_limit_max_wait", "UPSTREAM_RATE_LIMIT_MAX_WAIT", 0, 10*time.Minute); err != nil {
		return nil, err
	}
	if err := rate.Valid(); err != nil {
		return nil, fmt.Errorf("settings: UPSTREAM_RATE_LIMIT_RPM/BURST/MAX_WAIT: %w", err)
	}
	u.RateLimit = rate
	return u, nil
}

// cliVersion reads the pinned Claude Code version: empty, or three dot-separated
// numbers. The floor is applied where the version is used.
func cliVersion(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("UPSTREAM_CLAUDE_CLI_VERSION must be x.y.z")
	}
	for _, p := range parts {
		if p == "" || len(p) > 6 {
			return "", fmt.Errorf("UPSTREAM_CLAUDE_CLI_VERSION must be x.y.z")
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return "", fmt.Errorf("UPSTREAM_CLAUDE_CLI_VERSION must be x.y.z")
			}
		}
	}
	return raw, nil
}

// curveSet reads the curve setting: x25519 offers only that group, default
// offers the standard list.
func curveSet(raw string) ([]tls.CurveID, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "x25519":
		return []tls.CurveID{tls.X25519}, nil
	case "default":
		return nil, nil
	}
	return nil, fmt.Errorf("UPSTREAM_TLS_CURVE must be x25519 or default")
}
