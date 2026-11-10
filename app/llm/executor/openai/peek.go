package openai

import (
	"bytes"
	"context"
	"io"
	"strings"
	"time"

	"rllmx/app/llm/executor"
)

// Strings that mark an error event inside a stream. A retry one means the
// upstream is busy and the same account may be tried again; a capacity one means
// this model is full for everybody, so another account should serve.
var (
	peekRetry    = patterns("server_is_overloaded", "service_unavailable_error")
	peekCapacity = patterns("selected model is at capacity", "model_at_capacity")
	// peekOutput marks real output: from here on nothing is retried.
	peekOutput = patterns(
		"event: response.output_text.delta", "event: response.function_call_arguments.delta",
		`"type":"response.output_text.delta"`, `"type":"response.function_call_arguments.delta"`,
	)
)

// patterns returns the lower-case byte form the scan searches for.
func patterns(s ...string) [][]byte {
	out := make([][]byte, len(s))
	for i, v := range s {
		out[i] = []byte(strings.ToLower(v))
	}
	return out
}

type peekVerdict int

const (
	peekClean peekVerdict = iota
	peekBusy
	peekFull
)

// peekStream reads the start of a stream answer. A busy or full verdict means
// the body is closed and nothing is to be replayed; a clean one returns a body
// that yields every byte again.
func peekStream(ctx context.Context, body io.ReadCloser, idle time.Duration) (peekVerdict, io.ReadCloser) {
	var scan peekScan
	verdict := peekClean
	done := 0
	_, rest, _ := executor.PeekUntil(ctx, body, idle, func(seen []byte) (int, bool, bool) {
		v, output := scan.add(seen[done:])
		done = len(seen)
		if v != peekClean {
			verdict = v
			return 0, true, false
		}
		return 0, false, output
	})
	return verdict, rest
}

// peekScan looks for an error event that comes before any output, lowercasing
// only the bytes it has not seen. A string that straddles two reads is found
// because each search starts a pattern's length before the new bytes.
type peekScan struct {
	low []byte
}

// longestPeek is the longest string add searches for.
var longestPeek = func() int {
	n := 0
	for _, set := range [][][]byte{peekRetry, peekCapacity, peekOutput} {
		for _, s := range set {
			n = max(n, len(s))
		}
	}
	return n
}()

// add takes the next bytes and reports a busy or full verdict, or that real
// output has started. Once a scan reports output or a verdict the caller stops.
func (s *peekScan) add(chunk []byte) (verdict peekVerdict, output bool) {
	from := max(len(s.low)-longestPeek+1, 0)
	for _, c := range chunk {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		s.low = append(s.low, c)
	}
	window := s.low[from:]
	out := firstIndex(window, peekOutput)
	if i := firstIndex(window, peekCapacity); i >= 0 && (out < 0 || i < out) {
		return peekFull, false
	}
	if i := firstIndex(window, peekRetry); i >= 0 && (out < 0 || i < out) {
		return peekBusy, false
	}
	return peekClean, out >= 0
}

// firstIndex is the lowest position of any pattern in b, or -1.
func firstIndex(b []byte, patterns [][]byte) int {
	best := -1
	for _, p := range patterns {
		if i := bytes.Index(b, p); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}
