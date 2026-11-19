// Package saver shrinks one translated request before it is sent: tool output
// is compacted, bulky context may go through the Headroom sidecar, and a
// configured system prompt is appended. Every step fails open — a request is
// only ever made smaller or left as it was.
package rtk

import (
	"slices"
	"strings"
)

// Caps bound what one filter may read and emit, so a crafted tool result can
// never cost more memory here than it already did in the body.
const (
	rawCap          = 10 << 20 // a larger block is passed through untouched
	minCompress     = 500      // bytes; tiny results are not worth the rewrite
	detectWindow    = 1024     // how much of the head the detector reads
	diffHunkLines   = 100      // shown lines per hunk
	diffMaxLines    = 500      // shown lines for the whole diff
	logMaxLines     = 200
	dedupLineMax    = 2000
	grepPerFile     = 10
	listPerDir      = 10
	listTotalDirs   = 20
	statusMaxFiles  = 10
	statusMaxUntr   = 10
	treeMaxLines    = 200
	truncHead       = 120
	truncTail       = 60
	truncMinLines   = 250
	numberedRatio   = 0.7
	lsExtTop        = 5
	keepDeprecation = 3
	keepWarnings    = 5
)

// filter compresses one tool result.
type filter struct {
	name string
	fn   func(string) string
}

// filters is the set the detector may choose from.
var filters = []filter{
	{"git-log", gitLog},
	{"git-diff", gitDiff},
	{"git-status", gitStatus},
	{"build-output", buildOutput},
	{"grep", grepOutput},
	{"find", findOutput},
	{"tree", treeOutput},
	{"ls", lsOutput},
	{"search-list", searchList},
	{"read-numbered", readNumbered},
	{"dedup-log", dedupLog},
	{"smart-truncate", smartTruncate},
}

// detect picks the filter for a block of tool output, or nil when nothing fits.
// The order is the reference's: the most specific shape first, the generic
// collapse last.
func detect(text string) *filter {
	head := text
	if len(head) > detectWindow {
		head = cutUTF8(head, detectWindow)
	}
	switch {
	case reCommitHeader.MatchString(head):
		return byName("git-log")
	case reDiffHead.MatchString(head), reDiffHunk.MatchString(head):
		return byName("git-diff")
	case reStatusLine.MatchString(head):
		return byName("git-status")
	case reBuildLine.MatchString(head):
		return byName("build-output")
	case mostlyPorcelain(head):
		return byName("git-status")
	}
	lines := nonEmptyLines(head)
	if anyGrepLine(lines[:min(len(lines), 5)]) {
		return byName("grep")
	}
	if len(lines) >= 3 && allPathLike(lines) {
		return byName("find")
	}
	switch {
	case reTreeGlyph.MatchString(head):
		return byName("tree")
	case reLSTotal.MatchString(head), countLSRows(head) >= 3:
		return byName("ls")
	case reSearchHead.MatchString(head):
		return byName("search-list")
	case len(lines) >= truncMinLines && lineNumbered(lines):
		return byName("read-numbered")
	case len(lines) >= 5:
		return byName("dedup-log")
	}
	if strings.Count(text, "\n")+1 >= truncMinLines {
		return byName("smart-truncate")
	}
	return nil
}

func byName(name string) *filter {
	i := slices.IndexFunc(filters, func(f filter) bool { return f.name == name })
	if i < 0 {
		return nil
	}
	return &filters[i]
}

// Compress runs detection and accepts the result only when it is non-empty
// and strictly smaller than the input. It returns the text and the filter that
// was used, or the input unchanged and "" when nothing applied.
func Compress(text string) (string, string) {
	if len(text) < minCompress || len(text) > rawCap {
		return text, ""
	}
	f := detect(text)
	if f == nil {
		return text, ""
	}
	out := f.fn(text)
	if out == "" || len(out) >= len(text) {
		return text, ""
	}
	return out, f.name
}

func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

func nonEmptyLines(text string) []string {
	var out []string
	for l := range strings.SplitSeq(text, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
