package rtk

import (
	"fmt"
	"regexp"
	"strings"
)

// gitDiff compacts a unified diff: one line per file, hunk headers, at most
// diffHunkLines of each hunk and diffMaxLines in all, with a per-file change
// count. A reader keeps the shape of the change without the whole patch.
func gitDiff(diff string) string {
	out := make([]string, 0, 128)
	file, added, removed := "", 0, 0
	inHunk, shown, skipped := false, 0, 0
	truncated := false

	flushSkipped := func() {
		if skipped > 0 {
			out = append(out, fmt.Sprintf("  ... (%d lines truncated)", skipped))
			skipped = 0
			truncated = true
		}
	}
	fileCount := func() {
		if file != "" && (added > 0 || removed > 0) {
			out = append(out, fmt.Sprintf("  +%d -%d", added, removed))
		}
	}

lines:
	for line := range strings.SplitSeq(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushSkipped()
			fileCount()
			file = diffFile(line)
			out = append(out, "", file)
			added, removed, inHunk, shown = 0, 0, false, 0
		case strings.HasPrefix(line, "@@"):
			flushSkipped()
			inHunk, shown = true, 0
			out = append(out, "  "+line)
		case inHunk:
			switch {
			case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"), strings.HasPrefix(line, `\`):
			case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
				if line[0] == '+' {
					added++
				} else {
					removed++
				}
				if shown < diffHunkLines {
					out = append(out, "  "+line)
					shown++
				} else {
					skipped++
				}
			case shown > 0 && shown < diffHunkLines:
				// A blank context line is the diff's own trailing newline: it says
				// nothing and would land in the body as whitespace.
				if strings.TrimSpace(line) == "" {
					continue
				}
				out = append(out, "  "+line)
				shown++
			}
		}
		if len(out) >= diffMaxLines {
			out = append(out, "", "... (more changes truncated)")
			truncated = true
			break lines
		}
	}
	flushSkipped()
	fileCount()
	if truncated {
		out = append(out, "[the full diff was longer]")
	}
	return strings.Join(out, "\n")
}

// diffFile is the target path of a "diff --git a/old b/new" header.
func diffFile(line string) string {
	if _, after, found := strings.Cut(line, " b/"); found {
		return after
	}
	return "unknown"
}

// gitStatus compacts a status listing into a branch line and one counted block
// per kind, with capped file lists.
func gitStatus(in string) string {
	lines := strings.Split(in, "\n")
	if len(nonEmptyLines(in)) == 0 {
		return "Clean working tree"
	}
	branch := ""
	var staged, modified, untracked []string
	conflicts := 0
	for _, raw := range lines {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if name, ok := strings.CutPrefix(line, "On branch "); ok {
			branch = strings.Fields(name + " ")[0]
			continue
		}
		if rest, ok := strings.CutPrefix(line, "##"); ok {
			branch = strings.TrimSpace(rest)
			continue
		}
		if len(line) >= 3 && porcelainStatus(line[:2]) {
			status, path := line[:2], line[3:]
			switch {
			case status == "??":
				untracked = append(untracked, path)
			case status[0] == 'U':
				conflicts++
			default:
				if strings.ContainsAny(string(status[0]), "MADRC") {
					staged = append(staged, path)
				}
				if status[1] == 'M' || status[1] == 'D' {
					modified = append(modified, path)
				}
			}
			continue
		}
		if kind, path, ok := longStatus(line); ok {
			switch kind {
			case "both modified":
				conflicts++
			case "modified", "deleted":
				modified = append(modified, path)
			case "new file", "renamed":
				staged = append(staged, path)
			}
		}
	}

	var b strings.Builder
	if branch != "" {
		fmt.Fprintf(&b, "* %s\n", branch)
	}
	block(&b, "+ Staged", staged, statusMaxFiles)
	block(&b, "~ Modified", modified, statusMaxFiles)
	block(&b, "? Untracked", untracked, statusMaxUntr)
	if conflicts > 0 {
		fmt.Fprintf(&b, "conflicts: %d files\n", conflicts)
	}
	if len(staged)+len(modified)+len(untracked) == 0 && conflicts == 0 {
		b.WriteString("clean - nothing to commit\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// longStatus reads a "modified:   path" row of the long status form.
func longStatus(line string) (kind, path string, ok bool) {
	t := strings.TrimSpace(line)
	for _, k := range []string{"both modified", "new file", "modified", "deleted", "renamed"} {
		if rest, found := strings.CutPrefix(t, k+":"); found {
			return k, strings.TrimSpace(rest), true
		}
	}
	return "", "", false
}

func block(b *strings.Builder, title string, files []string, max int) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintf(b, "%s: %d files\n", title, len(files))
	for _, f := range files[:min(len(files), max)] {
		fmt.Fprintf(b, "   %s\n", f)
	}
	if len(files) > max {
		fmt.Fprintf(b, "   ... +%d more\n", len(files)-max)
	}
}

// git-log shapes.
var (
	reLogCommit  = regexp.MustCompile(`(?i)^commit [0-9a-f]{7,40}$`)
	reLogGraph   = regexp.MustCompile(`(?i)^[*|/\\ ]+commit [0-9a-f]{7,40}`)
	reLogMeta    = regexp.MustCompile(`(?i)^[*|/\\ ]*(Author|Date):`)
	reLogSubject = regexp.MustCompile(`^[*|/\\ ]*    \S`)
	reLogStat    = regexp.MustCompile(`^\d+ file\w* changed`)
	reLogOneLine = regexp.MustCompile(`(?i)^[*|/\\ ]+([0-9a-f]{7,40}\s+.+)`)
	reLogPlain   = regexp.MustCompile(`^[0-9a-f]{7,40}\s+`)
	reLogGraphly = regexp.MustCompile(`^[*|/\\ ]+$`)
)

// gitLog keeps commit headers, subjects, authors, dates and change counts, and
// drops bodies, decorations and embedded patches, up to logMaxLines.
func gitLog(in string) string {
	if in == "" {
		return ""
	}
	out := make([]string, 0, 64)
	skipped := 0
	inCommit, subjectSeen := false, false
	push := func(line string) {
		if len(out) < logMaxLines {
			out = append(out, line)
			return
		}
		skipped++
	}

	for raw := range strings.SplitSeq(in, "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		switch {
		case reLogCommit.MatchString(trimmed) || reLogGraph.MatchString(trimmed):
			inCommit, subjectSeen = true, false
			push(line)
		case inCommit && reLogMeta.MatchString(trimmed):
			push(trimmed)
		case inCommit && trimmed == "":
		case inCommit && !subjectSeen && reLogSubject.MatchString(line):
			push("  Subject: " + trimmed)
			subjectSeen = true
		case inCommit && reLogStat.MatchString(trimmed):
			push("  " + trimmed)
		case inCommit && strings.HasPrefix(trimmed, "diff --git "):
			push("  ... diff body omitted")
		case inCommit:
		case reLogOneLine.MatchString(trimmed):
			push(reLogOneLine.FindStringSubmatch(trimmed)[1])
		case reLogPlain.MatchString(trimmed):
			push(trimmed)
		case reLogGraphly.MatchString(trimmed):
		default:
			push(trimmed)
		}
	}
	if skipped > 0 {
		out = append(out, fmt.Sprintf("... (%d more lines)", skipped))
	}
	result := strings.Join(out, "\n")
	if result == "" || len(result) > len(in) {
		return in
	}
	return result
}
