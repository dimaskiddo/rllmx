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

func TestLoadMediaRefusesAValueOutOfRange(t *testing.T) {
	for _, v := range []string{"1s", "11m", "not a duration"} {
		_, err := LoadMedia(mediaValues(v))
		if err == nil || !strings.Contains(err.Error(), "MEDIA_VIDEO_TIMEOUT") {
			t.Errorf("%s: err %v", v, err)
		}
	}
}
