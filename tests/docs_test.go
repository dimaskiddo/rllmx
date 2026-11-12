package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repository root, one level above this package.
const root = ".."

// publicDocs are the tracked Markdown files a reader can open. AGENTS.md,
// CLAUDE.md and GEMINI.md are one file through a symlink.
func publicDocs(t *testing.T) []string {
	t.Helper()
	out := []string{"README.md", "AGENTS.md", "ROADMAP.md"}
	err := filepath.Walk(filepath.Join(root, "docs"), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// banned are strings that belong to private working records or the internal
// toolchain, never to a page a reader opens.
var banned = []string{
	"TASKS.md", "NOTES.md", "references/9router", "../rllm", "docs/prds", "prds/",
	"PRD ", "PRDs", "DWYOR", "remove-ai-marks",
}

// stepID is the public build numbering that no longer appears in a document.
var stepID = regexp.MustCompile(`\b1\.(?:9|10|11|12)[a-z]?\b`)

// internalPhases is the old phase vocabulary.
var internalPhases = regexp.MustCompile(`\bPhase [1-4]\b`)

// goPatch names a toolchain patch version; the docs and the build name only the
// minor release, so a reader never has to chase a patch number.
var goPatch = regexp.MustCompile(`\bGo 1\.2[0-9]\.[0-9]+`)

// advisory names a published vulnerability; a known finding belongs in the
// private working record, never in a public document.
var advisory = regexp.MustCompile(`(?m)\b(?:GO-20\d\d-\d+|CVE-\d{4}-\d+)\b`)

func TestDocsCarryNoPrivateWorkingRecord(t *testing.T) {
	for _, doc := range publicDocs(t) {
		raw, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		body := string(raw)
		for _, word := range banned {
			if strings.Contains(body, word) {
				t.Errorf("%s: %q belongs to a private record", doc, word)
			}
		}
		if loc := stepID.FindString(body); loc != "" {
			t.Errorf("%s: build step %q is internal", doc, loc)
		}
		if loc := internalPhases.FindString(body); loc != "" {
			t.Errorf("%s: phase %q is internal", doc, loc)
		}
		if loc := goPatch.FindString(body); loc != "" {
			t.Errorf("%s: %q names a patch version; write the minor release", doc, loc)
		}
		if loc := advisory.FindString(body); loc != "" {
			t.Errorf("%s: advisory %q belongs in the private working record", doc, loc)
		}
	}
}

// linkPattern is a relative Markdown link target.
var linkPattern = regexp.MustCompile(`\]\(([^)#\s][^)]*)\)`)

// codeSpan and fence are removed before links are read, so a model name such as
// `opus(high)` is never taken for a link.
var (
	codeSpan = regexp.MustCompile("`[^`\n]*`")
	fence    = regexp.MustCompile("(?s)```.*?```")
)

func TestDocsLinksResolve(t *testing.T) {
	for _, doc := range publicDocs(t) {
		raw, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(filepath.Join(root, doc))
		body := fence.ReplaceAllString(string(raw), "")
		body = codeSpan.ReplaceAllString(body, "")
		for _, m := range linkPattern.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			if target == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
				t.Errorf("%s: link %q does not resolve", doc, m[1])
			}
		}
	}
}

// maxProse is the longest a public paragraph may be, in words. Anything longer
// becomes a table, a list, or two paragraphs.
const maxProse = 60

// listItem starts a numbered or bulleted line; those are lists, not paragraphs.
var listItem = regexp.MustCompile(`^(?:[-*]|\d+\.)\s`)

// proseParas splits a document into prose paragraphs, ignoring code fences,
// tables, headings and list items.
func proseParas(body string) []string {
	body = fence.ReplaceAllString(body, "")
	var out []string
	var buf []string
	flush := func() {
		if len(buf) > 0 {
			out = append(out, strings.Join(buf, " "))
			buf = nil
		}
	}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			flush()
		case strings.HasPrefix(trimmed, "|"), strings.HasPrefix(trimmed, "#"),
			strings.HasPrefix(trimmed, ">"), strings.HasPrefix(trimmed, "```"),
			listItem.MatchString(trimmed):
			flush()
		default:
			buf = append(buf, trimmed)
		}
	}
	flush()
	return out
}

func TestProseParagraphsStayBrief(t *testing.T) {
	for _, doc := range publicDocs(t) {
		raw, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		for _, para := range proseParas(string(raw)) {
			if n := len(strings.Fields(para)); n > maxProse {
				t.Errorf("%s: a paragraph of %d words (limit %d): %.70s…", doc, n, maxProse, para)
			}
		}
	}
}

// findingsLead is the single line allowed above a Findings table.
const findingsLead = "One row per lesson; the decision is what the code does because of it."

func TestFindingsAreATableOrOneLine(t *testing.T) {
	for _, doc := range publicDocs(t) {
		raw, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		at := strings.Index(body, "\n## Findings")
		if at < 0 {
			continue
		}
		section := body[at+len("\n## Findings"):]
		if next := strings.Index(section, "\n## "); next >= 0 {
			section = section[:next]
		}
		var prose, rows, header []string
		for _, line := range strings.Split(section, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "|") {
				if len(header) == 0 && strings.Contains(trimmed, "Finding") && strings.Contains(trimmed, "|") {
					header = append(header, trimmed)
				}
				if strings.Trim(trimmed, "|- ") != "" {
					rows = append(rows, trimmed)
				}
				continue
			}
			if trimmed == "" {
				continue
			}
			prose = append(prose, trimmed)
		}
		if len(rows) == 0 {
			// No table: one sentence only, never a list of paragraphs.
			if len(prose) != 1 {
				t.Errorf("%s: Findings is %d lines without a table (leading: %.60s)", doc, len(prose), strings.Join(prose, " "))
			}
			continue
		}
		if len(header) == 0 || header[0] != "| Finding | Decision |" {
			t.Errorf("%s: Findings table header is %q, want \"| Finding | Decision |\"", doc, strings.Join(header, ""))
		}
		if len(prose) > 1 || (len(prose) == 1 && prose[0] != findingsLead) {
			t.Errorf("%s: prose outside the Findings table: %.70s", doc, strings.Join(prose, " "))
		}
	}
}
