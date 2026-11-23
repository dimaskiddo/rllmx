package settings

import (
	"strings"
	"testing"

	"rllmx/tests"
)

func TestParseLogOutput(t *testing.T) {
	cases := []struct {
		in     string
		file   bool
		stdout bool
	}{
		{"", true, true},
		{"both", true, true},
		{"file", true, false},
		{"stdout", false, true},
	}
	for _, c := range cases {
		got, err := ParseLogOutput(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.File != c.file || got.Stdout != c.stdout {
			t.Errorf("%q: file %t stdout %t", c.in, got.File, got.Stdout)
		}
	}
}

func TestParseLogOutputRefusesAnUnknownSink(t *testing.T) {
	for _, in := range []string{"console", "FILE", "none", "file,stdout"} {
		_, err := ParseLogOutput(in)
		if err == nil || !strings.Contains(err.Error(), "LOG_OUTPUT") {
			t.Errorf("%q: %v", in, err)
		}
	}
}

func TestCheckLoggingReadsTheStoredValue(t *testing.T) {
	if err := CheckLogging(tests.Config{Values: map[string]any{"logging.output": "file"}}); err != nil {
		t.Fatal(err)
	}
	if err := CheckLogging(tests.Config{Values: map[string]any{"logging.output": "terminal"}}); err == nil {
		t.Fatal("terminal names no sink")
	}
}

func TestALogLevelTypoStopsTheStart(t *testing.T) {
	for _, ok := range []string{"debug", "info", "warning", "error", " INFO ", ""} {
		if _, err := ParseLogLevel(ok); err != nil {
			t.Errorf("%q was refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"warn", "trace", "verbose", "silent"} {
		if _, err := ParseLogLevel(bad); err == nil {
			t.Errorf("%q was accepted; the framework would read it as debug", bad)
		}
	}
}
