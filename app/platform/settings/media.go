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
	Notices

	// VideoTimeout bounds one video HTTP round trip: a create or a poll, not the
	// job's own rendering time.
	VideoTimeout time.Duration
	// SearxngURL is the instance a search is sent to. Empty serves no SearXNG:
	// the address comes from the operator, never from a request.
	SearxngURL string
}

// LoadMedia reads the MEDIA_* settings. The video timeout is a tuning value and
// falls back to its default; the search address names a service the program may
// dial, so a bad one stops the start.
func LoadMedia(cfg config.Config) (*Media, error) {
	m := &Media{}
	m.VideoTimeout = duration(cfg, "media.video_timeout", "MEDIA_VIDEO_TIMEOUT", 5*time.Second, 10*time.Minute, 120*time.Second, &m.Notices)
	search, err := mediaURL(cfg.GetString("media.searxng_url"))
	if err != nil {
		return nil, err
	}
	m.SearxngURL = search
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
