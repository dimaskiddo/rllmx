package settings

import (
	"crypto/tls"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/config"

	"rllmx/app/platform/network"
	"rllmx/app/platform/ratelimit"
)

const (
	minConnectTimeout = time.Second
	maxConnectTimeout = 10 * time.Minute

	maxRateRPM   = 100000
	maxRateBurst = 10000
)

// Upstream is the validated view of the outbound network settings. The allow
// list comes from the environment on purpose: a dashboard session must not be
// able to widen where this program may connect.
type Upstream struct {
	Notices

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
	// KiroCreateResponse is when Kiro's second transport is used.
	KiroCreateResponse KiroCreateResponse
}

// KiroCreateResponse says when a Kiro request goes to the Responses transport
// instead of the one the IDE uses.
type KiroCreateResponse string

const (
	// KiroAuto falls back to it after a rate-limit answer the other transport
	// gives accounts that are enrolled in it.
	KiroAuto KiroCreateResponse = "auto"
	// KiroAlways uses it for every request.
	KiroAlways KiroCreateResponse = "always"
	// KiroOff never uses it.
	KiroOff KiroCreateResponse = "off"
)

// LoadUpstream reads the UPSTREAM_* settings. The allow list and only it names
// where the program may connect, so a bad entry stops the start; the rest tune
// the outbound call and fall back to their defaults with a notice.
func LoadUpstream(cfg config.Config) (*Upstream, error) {
	u := &Upstream{}
	u.ConnectTimeout = duration(cfg, "upstream.connect_timeout", "UPSTREAM_CONNECT_TIMEOUT", minConnectTimeout, maxConnectTimeout, 60*time.Second, &u.Notices)
	allow, err := network.ParseAllow(cfg.GetString("upstream.allow_private"))
	if err != nil {
		return nil, err
	}
	u.Allow = allow
	u.Curves = curveSet(cfg.GetString("upstream.tls_curve"), &u.Notices)
	u.ClaudeCLIVersion = cliVersion(cfg.GetString("upstream.claude_cli_version"), &u.Notices)
	rate := ratelimit.Config{
		RPM:   number(cfg, "upstream.rate_limit_rpm", "UPSTREAM_RATE_LIMIT_RPM", 0, maxRateRPM, 60, &u.Notices),
		Burst: number(cfg, "upstream.rate_limit_burst", "UPSTREAM_RATE_LIMIT_BURST", 1, maxRateBurst, 10, &u.Notices),
	}
	rate.MaxWait = duration(cfg, "upstream.rate_limit_max_wait", "UPSTREAM_RATE_LIMIT_MAX_WAIT", 0, 10*time.Minute, 30*time.Second, &u.Notices)
	u.RateLimit = rate
	u.KiroCreateResponse = kiroMode(cfg.GetString("upstream.kiro_create_response"), &u.Notices)
	return u, nil
}

// cliVersion reads the pinned Claude Code version: empty, or three dot-separated
// numbers. The floor is applied where the version is used.
func cliVersion(raw string, n *Notices) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		n.warn("UPSTREAM_CLAUDE_CLI_VERSION", "must be x.y.z", "the published version")
		return ""
	}
	for _, p := range parts {
		if p == "" || len(p) > 6 {
			n.warn("UPSTREAM_CLAUDE_CLI_VERSION", "must be x.y.z", "the published version")
			return ""
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				n.warn("UPSTREAM_CLAUDE_CLI_VERSION", "must be x.y.z", "the published version")
				return ""
			}
		}
	}
	return raw
}

// curveSet reads the curve setting: x25519 offers only that group, default
// offers the standard list. An unreadable value keeps x25519, the stricter set.
func curveSet(raw string, n *Notices) []tls.CurveID {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "x25519":
		return []tls.CurveID{tls.X25519}
	case "default":
		return nil
	}
	n.warn("UPSTREAM_TLS_CURVE", "must be x25519 or default", "x25519")
	return []tls.CurveID{tls.X25519}
}

// kiroMode reads the transport setting. A refusal names the variable and never
// the value.
func kiroMode(raw string, n *Notices) KiroCreateResponse {
	switch m := KiroCreateResponse(strings.ToLower(strings.TrimSpace(raw))); m {
	case "":
		return KiroAuto
	case KiroAuto, KiroAlways, KiroOff:
		return m
	}
	n.warn("UPSTREAM_KIRO_CREATE_RESPONSE", "must be auto, always or off", string(KiroAuto))
	return KiroAuto
}
