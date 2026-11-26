package settings

import (
	"errors"
	"net/url"
	"strings"

	"github.com/goravel/framework/contracts/config"
)

// Saver is the validated view of the SAVER_* settings.
type Saver struct {
	Notices

	// HeadroomURL is the sidecar's address when the settings row holds none.
	// Empty means the row's default stands.
	HeadroomURL string
}

// LoadSaver reads and checks the SAVER_* settings.
func LoadSaver(cfg config.Config) (*Saver, error) {
	raw, err := sidecarURL(cfg.GetString("saver.headroom_url"))
	if err != nil {
		return nil, err
	}
	return &Saver{HeadroomURL: raw}, nil
}

// sidecarURL reads the address of a service the operator runs: empty, or http(s)
// with a host, no user info and no fragment. The error never repeats the value.
func sidecarURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" || u.Fragment != "" ||
		(strings.ToLower(u.Scheme) != "https" && strings.ToLower(u.Scheme) != "http") {
		return "", errors.New("settings: SAVER_HEADROOM_URL: must be an http or https address with a host")
	}
	return u.String(), nil
}
