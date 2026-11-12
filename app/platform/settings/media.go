package settings

import (
	"time"

	"github.com/goravel/framework/contracts/config"
)

// Media is the validated view of the MEDIA_* settings.
type Media struct {
	// VideoTimeout bounds one video HTTP round trip: a create or a poll, not the
	// job's own rendering time.
	VideoTimeout time.Duration
}

// LoadMedia reads and checks the MEDIA_* settings. A value out of range stops
// the start.
func LoadMedia(cfg config.Config) (*Media, error) {
	m := &Media{}
	var err error
	if m.VideoTimeout, err = duration(cfg, "media.video_timeout", "MEDIA_VIDEO_TIMEOUT", 5*time.Second, 10*time.Minute); err != nil {
		return nil, err
	}
	return m, nil
}
