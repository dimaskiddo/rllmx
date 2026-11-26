package settings

import (
	"strings"
	"testing"
	"time"

	"rllmx/tests"
)

func mediaValues(video string) tests.Config {
	return tests.Config{Values: map[string]any{"media.video_timeout": video}}
}

func TestLoadMediaReadsTheDefaults(t *testing.T) {
	m, err := LoadMedia(mediaValues("120s"))
	if err != nil {
		t.Fatal(err)
	}
	if m.VideoTimeout != 120*time.Second {
		t.Fatalf("%+v", m)
	}
}

func TestLoadMediaWarnsOnAVideoTimeoutOutOfRange(t *testing.T) {
	for _, v := range []string{"1s", "11m", "not a duration"} {
		m, err := LoadMedia(mediaValues(v))
		if err != nil {
			t.Errorf("%s stopped the start: %v", v, err)
			continue
		}
		if m.VideoTimeout != 120*time.Second || len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "MEDIA_VIDEO_TIMEOUT") {
			t.Errorf("%s: %v %v", v, m.VideoTimeout, m.Warnings)
		}
	}
}
