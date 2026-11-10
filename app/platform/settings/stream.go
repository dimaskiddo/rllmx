package settings

import (
	"time"

	"github.com/goravel/framework/contracts/config"
)

// Stream is the validated view of the STREAM_* settings.
type Stream struct {
	FirstChunk time.Duration
	Stall      time.Duration
	UsageWait  time.Duration
}

// LoadStream reads and checks the STREAM_* settings. A value out of range stops
// the start.
func LoadStream(cfg config.Config) (*Stream, error) {
	s := &Stream{}
	var err error
	if s.FirstChunk, err = duration(cfg, "stream.first_chunk_timeout", "STREAM_FIRST_CHUNK_TIMEOUT", 5*time.Second, time.Hour); err != nil {
		return nil, err
	}
	if s.Stall, err = duration(cfg, "stream.stall_timeout", "STREAM_STALL_TIMEOUT", 5*time.Second, time.Hour); err != nil {
		return nil, err
	}
	if s.UsageWait, err = duration(cfg, "stream.usage_wait", "STREAM_USAGE_WAIT", 100*time.Millisecond, 30*time.Second); err != nil {
		return nil, err
	}
	return s, nil
}
