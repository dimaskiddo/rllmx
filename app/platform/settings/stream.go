package settings

import (
	"time"

	"github.com/goravel/framework/contracts/config"
)

// Stream is the validated view of the STREAM_* settings.
type Stream struct {
	Notices

	FirstChunk time.Duration
	Stall      time.Duration
	UsageWait  time.Duration
	Keepalive  time.Duration
}

// LoadStream reads the STREAM_* settings. Each is a tuning value: an unreadable
// or out-of-range one uses its default and is reported.
func LoadStream(cfg config.Config) (*Stream, error) {
	s := &Stream{}
	s.FirstChunk = duration(cfg, "stream.first_chunk_timeout", "STREAM_FIRST_CHUNK_TIMEOUT", 5*time.Second, time.Hour, 200*time.Second, &s.Notices)
	s.Stall = duration(cfg, "stream.stall_timeout", "STREAM_STALL_TIMEOUT", 5*time.Second, time.Hour, 6*time.Minute, &s.Notices)
	s.UsageWait = duration(cfg, "stream.usage_wait", "STREAM_USAGE_WAIT", 100*time.Millisecond, 30*time.Second, 3*time.Second, &s.Notices)
	s.Keepalive = duration(cfg, "stream.keepalive", "STREAM_KEEPALIVE", 0, 5*time.Minute, 15*time.Second, &s.Notices)
	return s, nil
}
