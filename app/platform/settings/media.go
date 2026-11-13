package settings

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/goravel/framework/contracts/config"
)

// Media is the validated view of the MEDIA_* settings.
type Media struct {
	// VideoTimeout bounds one video HTTP round trip: a create or a poll, not the
	// job's own rendering time.
	VideoTimeout time.Duration
	// SearxngURL is the instance a search is sent to. Empty serves no SearXNG:
	// the address comes from the operator, never from a request.
	SearxngURL string
}

// LoadMedia reads and checks the MEDIA_* settings. A value out of range stops
// the start.
func LoadMedia(cfg config.Config) (*Media, error) {
	m := &Media{}
	var err error
	if m.VideoTimeout, err = duration(cfg, "media.video_timeout", "MEDIA_VIDEO_TIMEOUT", 5*time.Second, 10*time.Minute); err != nil {
		return nil, err
	}
	if m.SearxngURL, err = mediaURL(cfg.GetString("media.searxng_url")); err != nil {
		return nil, err
	}
	return m, nil
}

// mediaURL reads the address of a service the operator runs: empty, or http(s)
// with a host, no user info and no fragment. The error never repeats the value.
func mediaURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Hostname() == "" || u.Fragment != "" ||
		(strings.ToLower(u.Scheme) != "https" && strings.ToLower(u.Scheme) != "http") {
		return "", errors.New("settings: MEDIA_SEARXNG_URL: must be an http or https address with a host")
	}
	return u.String(), nil
}
