package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

var (
	docLink     = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	docFence    = regexp.MustCompile("(?s)```.*?```")
	docCodeSpan = regexp.MustCompile("`[^`\n]*`")
	docHeading  = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	docScheme   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
)

// docSlug is GitHub's heading anchor: lower case, letters, digits, dashes and
// underscores kept, a space becomes a dash, everything else dropped.
func docSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// A link a reader follows is part of the documentation. The README sends them
// to the migration guide, the guide sends them back, and a rename either one
// forgets has to fail here rather than in a reader's browser.
func TestEveryDocLinkResolves(t *testing.T) {
	files := []string{"../README.md", "../ROADMAP.md", "../AGENTS.md"}
	for _, glob := range []string{"docs/*.md", "docs/design/*.md"} {
		found, err := filepath.Glob(filepath.Join("..", glob))
		if err != nil || len(found) == 0 {
			t.Fatalf("no files for %s: %v", glob, err)
		}
		files = append(files, found...)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := docCodeSpan.ReplaceAllString(docFence.ReplaceAllString(string(body), ""), "")
		for _, m := range docLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.HasPrefix(target, "#") || docScheme.MatchString(target) {
				continue
			}
			path, fragment, _ := strings.Cut(target, "#")
			resolved := filepath.Join(filepath.Dir(file), path)
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s links to %s, which does not exist", file, target)
				continue
			}
			if fragment == "" || !strings.HasSuffix(path, ".md") {
				continue
			}
			linked, err := os.ReadFile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			ok := false
			for _, h := range docHeading.FindAllStringSubmatch(string(linked), -1) {
				if docSlug(h[1]) == fragment {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s links to %s, which has no heading with that anchor", file, target)
			}
		}
	}
}

// The index is the first thing a reader opens, so a status there that disagrees
// with the doc's own line sends the reader to the wrong conclusion.
func TestTheDesignIndexMatchesEachDoc(t *testing.T) {
	dir := filepath.Join("..", "docs", "design")
	files, err := filepath.Glob(filepath.Join(dir, "[0-9][0-9]-*.md"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no design docs found: %v", err)
	}
	status := map[string]string{}
	for _, name := range files {
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?m)^\*\*Status:\*\*\s*([\w/]+)`).FindStringSubmatch(string(body))
		if m == nil {
			t.Errorf("%s has no Status line", filepath.Base(name))
			continue
		}
		status[filepath.Base(name)[:2]] = m[1]
	}
	index, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile(`(?m)^\| (\d\d) \| \[[^\]]+\]\(([^)]+)\) \| ([\w/]+) \|$`).FindAllStringSubmatch(string(index), -1)
	if len(rows) != len(status) {
		t.Errorf("the index lists %d docs, %d exist", len(rows), len(status))
	}
	for _, row := range rows {
		id, file, want := row[1], row[2], row[3]
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			t.Errorf("the index points at %s, which does not exist", file)
		}
		if got := status[id]; got != want {
			t.Errorf("doc %s is %q in the index and %q in the file", id, want, got)
		}
	}
}
