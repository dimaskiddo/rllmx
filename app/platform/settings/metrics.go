package settings

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goravel/framework/contracts/config"
)

const (
	// Short credentials are guessed at line rate, and a public scrape endpoint
	// is the one place that matters.
	minTokenLen    = 32
	minPasswordLen = 16
)

// Metrics is the validated view of the METRICS_* settings. A zero value serves
// no endpoint.
type Metrics struct {
	Notices

	Token    string
	Username string
	Password string
}

// LoadMetrics reads and checks the METRICS_* settings. A half-configured pair
// or a short secret stops the start: an endpoint left open is worse than one
// that never came up.
func LoadMetrics(cfg config.Config) (*Metrics, error) {
	m := &Metrics{
		Token:    strings.TrimSpace(cfg.GetString("metrics.token")),
		Username: strings.TrimSpace(cfg.GetString("metrics.username")),
		Password: strings.TrimSpace(cfg.GetString("metrics.password")),
	}
	for _, v := range []struct{ value, env string }{
		{m.Token, "METRICS_TOKEN"}, {m.Username, "METRICS_USERNAME"}, {m.Password, "METRICS_PASSWORD"},
	} {
		if err := checkPrint(v.value, v.env); err != nil {
			return nil, err
		}
	}
	if (m.Username == "") != (m.Password == "") {
		return nil, errors.New("settings: METRICS_USERNAME: needs both a username and a password")
	}
	if strings.Contains(m.Username, ":") {
		return nil, errors.New("settings: METRICS_USERNAME: must not contain a colon")
	}
	if m.Token != "" && len(m.Token) < minTokenLen {
		return nil, fmt.Errorf("settings: METRICS_TOKEN: must be at least %d characters", minTokenLen)
	}
	if m.Password != "" && len(m.Password) < minPasswordLen {
		return nil, fmt.Errorf("settings: METRICS_PASSWORD: must be at least %d characters", minPasswordLen)
	}
	return m, nil
}

// Enabled reports whether the endpoint is served at all.
func (m *Metrics) Enabled() bool { return m != nil && (m.Token != "" || m.Username != "") }
