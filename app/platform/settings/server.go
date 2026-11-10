package settings

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/config"

	"rllmx/app/platform/network"
)

const (
	minReadHeaderTimeout = time.Second
	maxReadHeaderTimeout = 5 * time.Minute
	minIdleTimeout       = time.Second
	maxIdleTimeout       = time.Hour
	minShutdownGrace     = time.Second
	maxShutdownGrace     = 10 * time.Minute
)

// Server is the validated view of the listener and guard settings. The values
// come from the framework's Config, so the environment over .env precedence and
// defaults are the framework's own.
type Server struct {
	Host string
	Port int
	// URL is the base address people reach the gateway at, or empty.
	URL string

	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ShutdownGrace     time.Duration
	HeaderLimitKiB    int

	// CertFile and KeyFile turn the listener into a TLS one when both are set.
	CertFile string
	KeyFile  string

	MaxBodyMB      int
	TrustLoopback  bool
	TrustedProxies []netip.Prefix
}

// LoadServer reads and checks the server settings. Errors name the variable and
// never repeat its value.
func LoadServer(cfg config.Config) (*Server, error) {
	s := &Server{
		Host:           cfg.GetString("http.host", "127.0.0.1"),
		Port:           cfg.GetInt("http.port", 20128),
		HeaderLimitKiB: cfg.GetInt("http.drivers.gin.header_limit", 64),
		CertFile:       cfg.GetString("http.tls.ssl.cert"),
		KeyFile:        cfg.GetString("http.tls.ssl.key"),
		MaxBodyMB:      cfg.GetInt("http.guard.max_body_mb", 128),
		TrustLoopback:  cfg.GetBool("http.guard.trust_loopback", true),
	}
	if s.Port < 1 || s.Port > 65535 {
		return nil, errors.New("settings: APP_PORT: must be between 1 and 65535")
	}
	if (s.CertFile == "") != (s.KeyFile == "") {
		return nil, errors.New("settings: http.tls.ssl: set both the certificate and the key, or neither")
	}
	if s.MaxBodyMB < 1 || s.MaxBodyMB > 1<<20 {
		return nil, fmt.Errorf("settings: SERVER_MAX_BODY_MB: must be between 1 and %d", 1<<20)
	}

	var err error
	if s.ReadHeaderTimeout, err = duration(cfg, "http.server.read_header_timeout", "HTTP_SERVER_READ_HEADER_TIMEOUT", minReadHeaderTimeout, maxReadHeaderTimeout); err != nil {
		return nil, err
	}
	if s.IdleTimeout, err = duration(cfg, "http.server.idle_timeout", "HTTP_SERVER_IDLE_TIMEOUT", minIdleTimeout, maxIdleTimeout); err != nil {
		return nil, err
	}
	if s.ShutdownGrace, err = duration(cfg, "http.server.shutdown_grace", "HTTP_SERVER_SHUTDOWN_GRACE", minShutdownGrace, maxShutdownGrace); err != nil {
		return nil, err
	}
	if s.TrustedProxies, err = parseProxies(cfg.GetString("http.guard.trusted_proxies")); err != nil {
		return nil, err
	}
	if s.URL, err = parseURL(cfg.GetString("http.url")); err != nil {
		return nil, err
	}
	return s, nil
}

func duration(cfg config.Config, key, env string, lo, hi time.Duration) (time.Duration, error) {
	d := cfg.GetDuration(key)
	if d < lo || d > hi {
		return 0, fmt.Errorf("settings: %s: must be a duration between %s and %s", env, lo, hi)
	}
	return d, nil
}

// parseProxies accepts addresses and CIDR ranges separated by commas. A bare
// address becomes a single-host range.
func parseProxies(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(item)
		if err != nil {
			return nil, errors.New("settings: SERVER_TRUSTED_PROXIES: an entry is not an address or CIDR range")
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

// parseURL returns the base URL in canonical form (lower-case scheme and host,
// no trailing slash). It is http or https with a host and nothing else, and
// https unless the host is this machine.
func parseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	bad := errors.New("settings: APP_URL: must be an http or https base URL such as https://gw.example.com")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", bad
	}
	if u.Path != "" && u.Path != "/" {
		return "", bad
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !network.IsLoopbackHost(u.Hostname()) {
			return "", errors.New("settings: APP_URL: must use https unless the host is this machine")
		}
	default:
		return "", bad
	}
	return scheme + "://" + strings.ToLower(u.Host), nil
}
