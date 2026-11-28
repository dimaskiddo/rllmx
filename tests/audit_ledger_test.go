package tests

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ledgerSection is NOTES.md's findings table.
func ledgerSection(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../NOTES.md")
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i := strings.Index(s, "### 5.2 Findings")
	if i < 0 {
		t.Fatal("NOTES.md has no findings section")
	}
	j := strings.Index(s[i:], "### 5.3")
	if j < 0 {
		t.Fatal("the findings section has no end")
	}
	return s[i : i+j]
}

// Every test a finding row cites must exist: a pin that names a test nobody
// wrote proves nothing about the fix.
func TestEveryTestCitedInTheLedgerExists(t *testing.T) {
	names := map[string]bool{}
	roots := []string{"../app", "../tests", "../bootstrap", "../config", "../routes", "../pkg"}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range regexp.MustCompile(`(?m)^func ((?:Test|Fuzz|Benchmark)\w+)`).FindAllStringSubmatch(string(raw), -1) {
				names[m[1]] = true
			}
			return nil
		})
	}
	var missing []string
	for _, m := range regexp.MustCompile("`((?:Test|Fuzz)[A-Za-z0-9_]+)`").FindAllStringSubmatch(ledgerSection(t), -1) {
		if !names[m[1]] {
			missing = append(missing, m[1])
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the ledger cites tests that do not exist: %v", missing)
	}
}

// The environment reference and the example file must name exactly the
// variables config/ reads, in both directions.
func TestTheEnvironmentDocumentsMatchTheCode(t *testing.T) {
	read := func(path string) string {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	code := map[string]bool{}
	configs, _ := filepath.Glob("../config/*.go")
	for _, p := range configs {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		for _, m := range regexp.MustCompile(`(?:config\.Env|envPath)\("([A-Z][A-Z0-9_]+)"`).FindAllStringSubmatch(read(p), -1) {
			code[m[1]] = true
		}
	}
	example := map[string]bool{}
	for _, l := range strings.Split(read("../.env.example"), "\n") {
		if m := regexp.MustCompile(`^\s*#?\s*([A-Z][A-Z0-9_]+)=`).FindStringSubmatch(l); m != nil {
			example[m[1]] = true
		}
	}
	doc := map[string]bool{}
	for _, l := range strings.Split(read("../docs/ENVIRONMENT.md"), "\n") {
		if !strings.HasPrefix(l, "|") {
			continue
		}
		for _, m := range regexp.MustCompile("`([A-Z][A-Z0-9_]+)`").FindAllStringSubmatch(l, -1) {
			doc[m[1]] = true
		}
	}
	var problems []string
	for name := range code {
		if !example[name] {
			problems = append(problems, "config reads "+name+", .env.example lacks it")
		}
		if !doc[name] {
			problems = append(problems, "config reads "+name+", ENVIRONMENT.md lacks it")
		}
	}
	for name := range example {
		if !code[name] {
			problems = append(problems, ".env.example lists "+name+", config never reads it")
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("%s", strings.Join(problems, "\n"))
	}
}

// The divergence index counts what the design docs hold, and names only real
// collisions.
func TestTheDivergenceIndexMatchesTheDocs(t *testing.T) {
	raw, err := os.ReadFile("../docs/DIVERGENCES.md")
	if err != nil {
		t.Fatal(err)
	}
	index := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\| (\d+) \| [^|]+ \| (\d+) \|`).FindAllStringSubmatch(string(raw), -1) {
		index[m[1]] = m[2]
	}
	docs, _ := filepath.Glob("../docs/design/[0-9][0-9]-*.md")
	total := 0
	for _, p := range docs {
		base := filepath.Base(p)
		num := base[:2]
		n := strings.TrimLeft(num, "0")
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		s := string(body)
		rows := 0
		if i := strings.Index(s, "## Divergences"); i >= 0 {
			sec := s[i:]
			if j := strings.Index(sec[3:], "\n## "); j >= 0 {
				sec = sec[:j+3]
			}
			numbered := regexp.MustCompile(`(?m)^\| *\d[0-9,\s–-]*\|`).FindAllString(sec, -1)
			rows = len(numbered)
			if rows == 0 {
				// Doc 18 numbers its rows inline (Dnnn) instead of in the first
				// cell, so its table's rows are counted directly.
				for _, l := range strings.Split(sec, "\n") {
					if strings.HasPrefix(l, "|") && !regexp.MustCompile(`^\|\s*-`).MatchString(l) && !strings.Contains(l, "Reference mechanism") {
						rows++
					}
				}
			}
		}
		want := index[n]
		if want == "" {
			continue
		}
		total += rows
		if len := strings.TrimSpace(want); len != "" && atoi(t, want) != rows {
			t.Errorf("%s: index says %s rows, the doc holds %d", base, want, rows)
		}
	}
	if m := regexp.MustCompile(`\*\*Total\*\* \| \| \*\*(\d+)\*\*`).FindStringSubmatch(string(raw)); m != nil && atoi(t, m[1]) != total {
		t.Errorf("the index total is %s, the docs hold %d", m[1], total)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			t.Fatalf("%q is not a number", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}
