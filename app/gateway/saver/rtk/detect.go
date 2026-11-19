package rtk

import (
	"regexp"
	"strings"
)

// The detector's shapes. Each is compiled once; the detector reads at most the
// first detectWindow bytes of a block.
var (
	reCommitHeader = regexp.MustCompile(`(?m)^[*|/\\ ]*commit [0-9a-f]{7,40}$`)
	reDiffHead     = regexp.MustCompile(`(?m)^diff --git `)
	reDiffHunk     = regexp.MustCompile(`(?m)^@@ `)
	reStatusLine   = regexp.MustCompile(`(?m)^On branch |^nothing to commit|^Changes (not |to be )|^Untracked files:`)
	rePorcelain    = regexp.MustCompile(`(?m)^[ MADRCU?!][ MADRCU?!] \S`)
	reBuildLine    = regexp.MustCompile(`(?im)^(npm (warn|error|ERR!)|yarn (warn|error)|\s*Compiling\s+\S+|\s*Downloading\s+\S+|added \d+ package|\[ERROR\]|BUILD (SUCCESS|FAILED)|\s*Finished\s+|Successfully (installed|built)|ERROR:)`)
	reTreeGlyph    = regexp.MustCompile(`├──|└──|│  `)
	reLSRow        = regexp.MustCompile(`(?m)^[-dlbcps][rwx-]{9}`)
	reLSTotal      = regexp.MustCompile(`(?m)^total \d+$`)
	reNumberedLine = regexp.MustCompile(`^\s*\d+\|`)
	reSearchHead   = regexp.MustCompile(`^Result of search in '[^']*' \(total \d+ files?\):`)
	reDigits       = regexp.MustCompile(`^\d+$`)
)

// mostlyPorcelain is true when most of the first lines are two-column status
// rows, the shape `git status --porcelain` prints.
func mostlyPorcelain(head string) bool {
	lines := nonEmptyLines(head)
	if len(lines) < 3 {
		return false
	}
	hits := 0
	for _, l := range lines {
		if rePorcelain.MatchString(l) {
			hits++
		}
	}
	return float64(hits)/float64(len(lines)) >= 0.6
}

// grepLine reports a "file:line:body" row, the shape a recursive grep prints.
func grepLine(line string) bool {
	first := strings.IndexByte(line, ':')
	if first < 0 {
		return false
	}
	second := strings.IndexByte(line[first+1:], ':')
	if second < 0 {
		return false
	}
	return reDigits.MatchString(line[first+1 : first+1+second])
}

func anyGrepLine(lines []string) bool {
	for _, l := range lines {
		if grepLine(l) {
			return true
		}
	}
	return false
}

// pathLike reports a bare path: no colon, and either absolute, relative or
// carrying a separator. A Windows drive prefix counts as a path by itself.
func pathLike(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	if len(t) > 2 && t[1] == ':' && (t[2] == '\\' || t[2] == '/') && isLetter(t[0]) {
		return true
	}
	if strings.ContainsRune(t, ':') {
		return false
	}
	return strings.HasPrefix(t, ".") || strings.HasPrefix(t, "/") || strings.ContainsRune(t, '/')
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// porcelainStatus reports a two-column git status code.
func porcelainStatus(code string) bool {
	const codes = " MADRCU?!"
	return len(code) == 2 && strings.IndexByte(codes, code[0]) >= 0 && strings.IndexByte(codes, code[1]) >= 0
}

func allPathLike(lines []string) bool {
	for _, l := range lines {
		if !pathLike(l) {
			return false
		}
	}
	return true
}

// lineNumbered is true when most of the first hundred lines carry the
// "  N|content" prefix a file reader prints.
func lineNumbered(lines []string) bool {
	hits, seen := 0, 0
	for _, l := range lines[:min(len(lines), 100)] {
		if l == "" {
			continue
		}
		seen++
		if reNumberedLine.MatchString(l) {
			hits++
		}
	}
	return seen >= 5 && float64(hits)/float64(seen) >= numberedRatio
}

func countLSRows(head string) int {
	return len(reLSRow.FindAllString(head, 4))
}
