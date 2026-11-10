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

func TestLoadStreamRefusesValuesOutOfRange(t *testing.T) {
	cases := map[string][3]string{
		"STREAM_FIRST_CHUNK_TIMEOUT": {"1s", "360s", "3s"},
		"STREAM_STALL_TIMEOUT":       {"200s", "2h", "3s"},
		"STREAM_USAGE_WAIT":          {"200s", "360s", "50ms"},
	}
	for name, v := range cases {
		_, err := LoadStream(streamValues(v[0], v[1], v[2]))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err %v", name, err)
		}
	}
	if _, err := LoadStream(streamValues("200s", "360s", "31s")); err == nil {
		t.Error("a usage wait over 30s was accepted")
	}
	if _, err := LoadStream(streamValues("not a duration", "360s", "3s")); err == nil {
		t.Error("a malformed duration was accepted")
	}
}
