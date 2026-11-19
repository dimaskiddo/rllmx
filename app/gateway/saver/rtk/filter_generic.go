package rtk

import (
	"fmt"
	"strings"
)

// readNumbered keeps the head and tail of a line-numbered file dump, the shape
// a file-reading tool returns, with the middle counted.
func readNumbered(in string) string {
	lines := strings.Split(in, "\n")
	if len(lines) < truncMinLines {
		return in
	}
	head := lines[:truncHead]
	tail := lines[len(lines)-truncTail:]
	cut := len(lines) - len(head) - len(tail)
	return strings.Join(head, "\n") + fmt.Sprintf("\n... +%d lines truncated (file continues)\n", cut) + strings.Join(tail, "\n")
}

// smartTruncate is the fallback for an unstructured block: keep the head and
// the tail, count what went.
func smartTruncate(in string) string {
	lines := strings.Split(in, "\n")
	if len(lines) < truncMinLines {
		return in
	}
	head := lines[:truncHead]
	tail := lines[len(lines)-truncTail:]
	cut := len(lines) - len(head) - len(tail)
	return strings.Join(head, "\n") + fmt.Sprintf("\n... +%d lines truncated\n", cut) + strings.Join(tail, "\n")
}

// dedupLog collapses consecutive repeats, keeps one blank line between blocks
// and caps the height, for a log with more noise than signal.
func dedupLog(in string) string {
	out := make([]string, 0, 64)
	prev := ""
	run := 0
	flush := func() {
		if prev != "" && run > 1 {
			out = append(out, fmt.Sprintf("  ... (%d duplicate lines)", run-1))
		}
	}
	blanks := 0
	for line := range strings.SplitSeq(in, "\n") {
		if strings.TrimSpace(line) == "" {
			if blanks < 1 {
				out = append(out, line)
			}
			blanks++
			flush()
			prev, run = "", 0
			continue
		}
		blanks = 0
		if line == prev {
			run++
			continue
		}
		flush()
		out = append(out, line)
		prev, run = line, 1
		if len(out) >= dedupLineMax {
			out = append(out, fmt.Sprintf("... (truncated at %d lines)", dedupLineMax))
			return strings.Join(out, "\n")
		}
	}
	flush()
	return strings.Join(out, "\n")
}
