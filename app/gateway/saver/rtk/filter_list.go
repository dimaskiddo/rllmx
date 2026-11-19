package rtk

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// grepOutput groups "file:line:body" rows by file: a count, each file with its
// first grepPerFile matches, and the remainder counted.
func grepOutput(in string) string {
	type match struct{ line, body string }
	byFile := map[string][]match{}
	total := 0
	for line := range strings.SplitSeq(in, "\n") {
		first := strings.IndexByte(line, ':')
		if first < 0 {
			continue
		}
		second := strings.IndexByte(line[first+1:], ':')
		if second < 0 {
			continue
		}
		num := line[first+1 : first+1+second]
		if _, err := strconv.Atoi(num); err != nil {
			continue
		}
		file, body := line[:first], line[first+1+second+1:]
		byFile[file] = append(byFile[file], match{num, strings.TrimSpace(body)})
		total++
	}
	if total == 0 {
		return in
	}
	files := make([]string, 0, len(byFile))
	for f := range byFile {
		files = append(files, f)
	}
	sort.Strings(files)

	var b strings.Builder
	fmt.Fprintf(&b, "%d matches in %dF:\n\n", total, len(files))
	for _, f := range files {
		matches := byFile[f]
		fmt.Fprintf(&b, "[file] %s (%d):\n", f, len(matches))
		for _, m := range matches[:min(len(matches), grepPerFile)] {
			fmt.Fprintf(&b, "%6s: %s\n", m.line, m.body)
		}
		if len(matches) > grepPerFile {
			fmt.Fprintf(&b, "  +%d\n", len(matches)-grepPerFile)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// findOutput groups paths by directory, shows each basename, and caps the
// directories and the names per directory.
func findOutput(in string) string {
	var paths []string
	for l := range strings.SplitSeq(in, "\n") {
		if strings.TrimSpace(l) != "" {
			paths = append(paths, l)
		}
	}
	if len(paths) == 0 {
		return in
	}
	byDir := map[string][]string{}
	for _, p := range paths {
		dir, name := splitPath(p)
		byDir[dir] = append(byDir[dir], name)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var b strings.Builder
	fmt.Fprintf(&b, "%d files in %d dirs:\n\n", len(paths), len(dirs))
	for _, dir := range dirs[:min(len(dirs), listTotalDirs)] {
		names := byDir[dir]
		fmt.Fprintf(&b, "%s  (%d)\n", withSlash(dir), len(names))
		for _, n := range names[:min(len(names), listPerDir)] {
			fmt.Fprintf(&b, "  %s\n", n)
		}
		if len(names) > listPerDir {
			fmt.Fprintf(&b, "  +%d\n", len(names)-listPerDir)
		}
	}
	if len(dirs) > listTotalDirs {
		fmt.Fprintf(&b, "\n+%d more dirs\n", len(dirs)-listTotalDirs)
	}
	return b.String()
}

// splitPath gives a path's directory and base name, with "." for a bare name.
func splitPath(p string) (string, string) {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return ".", p
	}
	dir := p[:i]
	if dir == "" {
		dir = "/"
	}
	return dir, p[i+1:]
}

// withSlash renders a directory with one trailing separator, also for a
// Windows path.
func withSlash(dir string) string {
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, `\`) {
		return dir
	}
	return dir + "/"
}

// ls shapes.
var (
	reLSDate = regexp.MustCompile(`\s+(Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2}\s+(\d{4}|\d{2}:\d{2})\s+`)
)

// lsNoiseDirs are entries an agent never needs summarized.
var lsNoiseDirs = map[string]bool{
	"node_modules": true, ".git": true, "target": true, "__pycache__": true, ".next": true, "dist": true,
	"build": true, ".cache": true, ".turbo": true, ".vercel": true, ".pytest_cache": true, ".mypy_cache": true,
	".tox": true, ".venv": true, "venv": true, "env": true, "coverage": true, ".nyc_output": true, ".DS_Store": true,
	"Thumbs.db": true, ".idea": true, ".vscode": true, ".vs": true, "*.egg-info": true, ".eggs": true,
}

// lsOutput compacts a long listing into directories, files with sizes, and an
// extension summary.
func lsOutput(in string) string {
	var dirs []string
	type entry struct{ name, size string }
	var files []entry
	byExt := map[string]int{}
	for line := range strings.SplitSeq(in, "\n") {
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		fileType, size, name, ok := parseLS(line)
		if !ok || name == "." || name == ".." || lsNoiseDirs[name] {
			continue
		}
		switch fileType {
		case 'd':
			dirs = append(dirs, name)
		case '-', 'l':
			if dot := strings.LastIndexByte(name, '.'); dot > 0 {
				byExt[name[dot:]]++
			} else {
				byExt["no ext"]++
			}
			files = append(files, entry{name, humanSize(size)})
		}
	}
	if len(dirs) == 0 && len(files) == 0 {
		return in
	}

	var b strings.Builder
	for _, d := range dirs {
		b.WriteString(d)
		b.WriteString("/\n")
	}
	for _, f := range files {
		fmt.Fprintf(&b, "%s  %s\n", f.name, f.size)
	}
	fmt.Fprintf(&b, "\nSummary: %d files, %d dirs", len(files), len(dirs))
	if len(byExt) > 0 {
		type ext struct {
			name string
			n    int
		}
		all := make([]ext, 0, len(byExt))
		for name, n := range byExt {
			all = append(all, ext{name, n})
		}
		sort.Slice(all, func(i, j int) bool {
			if all[i].n != all[j].n {
				return all[i].n > all[j].n
			}
			return all[i].name < all[j].name
		})
		parts := make([]string, 0, lsExtTop)
		for _, e := range all[:min(len(all), lsExtTop)] {
			parts = append(parts, fmt.Sprintf("%d %s", e.n, e.name))
		}
		fmt.Fprintf(&b, " (%s", strings.Join(parts, ", "))
		if len(all) > lsExtTop {
			fmt.Fprintf(&b, ", +%d more", len(all)-lsExtTop)
		}
		b.WriteString(")")
	}
	return b.String()
}

// parseLS reads one long-listing row: permissions, the rightmost number before
// the date as the size, then the name.
func parseLS(line string) (fileType byte, size int, name string, ok bool) {
	loc := reLSDate.FindStringIndex(line)
	if loc == nil {
		return 0, 0, "", false
	}
	fields := strings.Fields(line[:loc[0]])
	if len(fields) < 4 {
		return 0, 0, "", false
	}
	for i := len(fields) - 1; i >= 0; i-- {
		if n, err := strconv.Atoi(fields[i]); err == nil {
			size = n
			break
		}
	}
	return fields[0][0], size, line[loc[1]:], true
}

func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return strconv.Itoa(n) + "B"
}

// treeOutput drops a tree's summary line and blank edges, and caps the height.
func treeOutput(in string) string {
	out := make([]string, 0, 64)
	for line := range strings.SplitSeq(in, "\n") {
		if strings.Contains(line, "director") && strings.Contains(line, "file") {
			continue
		}
		if strings.TrimSpace(line) == "" && len(out) == 0 {
			continue
		}
		out = append(out, line)
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	if len(out) > treeMaxLines {
		cut := len(out) - treeMaxLines
		return strings.Join(out[:treeMaxLines], "\n") + fmt.Sprintf("\n... +%d more lines", cut)
	}
	return strings.Join(out, "\n")
}

// searchList compacts a search-result list grouped by directory.
func searchList(in string) string {
	lines := strings.Split(in, "\n")
	header := lines[0]
	var paths []string
	for _, raw := range lines[1:] {
		if p, ok := strings.CutPrefix(strings.TrimSpace(raw), "- "); ok {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return in
	}
	byDir := map[string][]string{}
	for _, p := range paths {
		dir, name := splitPath(p)
		byDir[dir] = append(byDir[dir], name)
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%d files in %d dirs:\n\n", header, len(paths), len(dirs))
	for _, dir := range dirs[:min(len(dirs), listTotalDirs)] {
		names := byDir[dir]
		fmt.Fprintf(&b, "%s/ (%d):\n", dir, len(names))
		for _, n := range names[:min(len(names), listPerDir)] {
			fmt.Fprintf(&b, "  %s\n", n)
		}
		if len(names) > listPerDir {
			fmt.Fprintf(&b, "  +%d\n", len(names)-listPerDir)
		}
		b.WriteString("\n")
	}
	if len(dirs) > listTotalDirs {
		fmt.Fprintf(&b, "+%d more dirs\n", len(dirs)-listTotalDirs)
	}
	return strings.TrimRight(b.String(), "\n")
}
