package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

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
