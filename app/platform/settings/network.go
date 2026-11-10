package settings

import (
	"time"

	"github.com/goravel/framework/contracts/config"

	"rllmx/app/platform/network"
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
	return u, nil
}
