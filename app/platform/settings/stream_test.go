package settings

import (
	"strings"
	"testing"
	"time"

	"rllmx/tests"
)

func streamValues(first, stall, wait string) tests.Config {
	return tests.Config{Values: map[string]any{
		"stream.first_chunk_timeout": first, "stream.stall_timeout": stall, "stream.usage_wait": wait,
	}}
}

func TestLoadStreamReadsTheDefaults(t *testing.T) {
	s, err := LoadStream(streamValues("200s", "360s", "3s"))
	if err != nil {
		t.Fatal(err)
	}
	if s.FirstChunk != 200*time.Second || s.Stall != 360*time.Second || s.UsageWait != 3*time.Second {
		t.Fatalf("%+v", s)
	}
}

// Every stream setting is a tuning value: one that is out of range or unreadable
// keeps the default and is reported, and the stream still runs.
func TestLoadStreamWarnsAndKeepsTheDefaults(t *testing.T) {
	cases := map[string][3]string{
		"STREAM_FIRST_CHUNK_TIMEOUT":  {"1s", "360s", "3s"},
		"STREAM_STALL_TIMEOUT":        {"200s", "2h", "3s"},
		"STREAM_USAGE_WAIT":           {"200s", "360s", "50ms"},
		"STREAM_USAGE_WAIT ":          {"200s", "360s", "31s"},
		"STREAM_FIRST_CHUNK_TIMEOUT ": {"not a duration", "360s", "3s"},
	}
	for name, v := range cases {
		s, err := LoadStream(streamValues(v[0], v[1], v[2]))
		if err != nil {
			t.Errorf("%s: a tuning value stopped the start: %v", name, err)
			continue
		}
		if len(s.Warnings) != 1 || !strings.Contains(s.Warnings[0], strings.TrimSpace(name)) {
			t.Errorf("%s: warnings = %v", name, s.Warnings)
		}
	}
	s, _ := LoadStream(streamValues("1s", "2h", "3s"))
	if s.FirstChunk != 200*time.Second || s.Stall != 6*time.Minute {
		t.Fatalf("defaults = %+v", s)
	}
}
