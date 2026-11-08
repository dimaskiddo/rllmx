package executor

import (
	"strings"
	"testing"
)

func scanAll(chunks ...string) (peekVerdict, bool) {
	var s peekScan
	for _, c := range chunks {
		if v, out := s.add([]byte(c)); v != peekClean || out {
			return v, out
		}
	}
	return peekClean, false
}

func TestAMarkerSplitAcrossReadsIsFound(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		want   peekVerdict
		output bool
	}{
		{"busy whole", []string{`{"code":"server_is_overloaded"}`}, peekBusy, false},
		{"busy split", []string{`{"code":"server_is_`, `overloaded"}`}, peekBusy, false},
		{"busy split by one byte", strings.Split(`x server_is_overloaded`, ""), peekBusy, false},
		{"full upper case", []string{`Selected Model Is At Capacity.`}, peekFull, false},
		{"full split", []string{`selected model is `, `AT CAPACITY`}, peekFull, false},
		{"output split", []string{`event: response.output_`, `text.delta`}, peekClean, true},
		{"output first", []string{`event: response.output_text.delta` + "\n" + `server_is_overloaded`}, peekClean, true},
		{"output before capacity", []string{`event: response.output_text.delta` + "\n" + `model_at_capacity`}, peekClean, true},
		{"error first", []string{`server_is_overloaded` + "\n" + `event: response.output_text.delta`}, peekBusy, false},
		{"nothing", []string{`data: {"a":1}`, `data: {"b":2}`}, peekClean, false},
	}
	for _, c := range cases {
		v, out := scanAll(c.chunks...)
		if v != c.want || out != c.output {
			t.Errorf("%s: verdict %v output %v, want %v %v", c.name, v, out, c.want, c.output)
		}
	}
}

func TestTheScanKeepsOnlyWhatItMustSearch(t *testing.T) {
	var s peekScan
	for range 100 {
		s.add([]byte(strings.Repeat("a", 1000)))
	}
	if len(s.low) != 100000 {
		t.Fatalf("kept %d bytes of lower-case text", len(s.low))
	}
}
