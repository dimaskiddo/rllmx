package skill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"rllmx/app/platform/file"
	"rllmx/app/platform/relevance"
	"rllmx/app/platform/text"
)

// Bounds on one catalog import, so a large or hostile checkout cannot make the
// sync allocate or run without limit.
const (
	maxCatalog     = 500
	maxCatalogFile = 64 << 10
	catalogDepth   = 3
)

// ECCSource and ECCBranch are the official catalog the dashboard syncs. The
// reference fixes the same address, so the page asks for neither.
const (
	ECCSource = "https://github.com/affaan-m/everything-claude-code.git"
	ECCBranch = "main"
)

// ECCSyncResult reports what one catalog sync did.
type ECCSyncResult struct {
	Total   int
	Updated int
	Skipped int
	Skills  []string
}

// SyncECC imports the Agent Skills folders found under srcDir into destDir,
// reading only SKILL.md text and writing our own manifest.json, prompt.md and
// _index.json. A folder whose manifest no longer says it came from the catalog
// is treated as the operator's and left alone unless force is set.
func SyncECC(ctx context.Context, srcDir, destDir string, force bool) (ECCSyncResult, error) {
	var res ECCSyncResult
	if srcDir == "" || destDir == "" {
		return res, errors.New("sync: a source and a destination are required")
	}
	info, err := os.Stat(srcDir)
	if err != nil || !info.IsDir() {
		return res, fmt.Errorf("sync: %s is not a directory", srcDir)
	}
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return res, err
	}
	root, err := os.OpenRoot(srcDir)
	if err != nil {
		return res, err
	}
	defer root.Close()
	seen := map[string]bool{}
	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && p != srcDir {
				return fs.SkipDir
			}
			return nil
		}
		// A link, a pipe or a device named SKILL.md is not a skill file: reading it
		// could pull in another file of this machine or never return.
		if d.Name() != "SKILL.md" || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil || strings.Count(filepath.ToSlash(rel), "/") > catalogDepth {
			return nil
		}
		if len(seen) >= maxCatalog {
			return fmt.Errorf("sync: more than %d skills", maxCatalog)
		}
		m, prompt, err := readSKILL(root, rel)
		if err != nil || seen[m.ID] {
			res.Skipped++
			return nil
		}
		res.Total++
		written, err := writeECC(destDir, m, prompt, force)
		if err != nil {
			return err
		}
		if !written {
			res.Skipped++
			return nil
		}
		seen[m.ID] = true
		res.Updated++
		res.Skills = append(res.Skills, m.ID)
		return nil
	})
	if err != nil {
		return res, err
	}
	if err := writeIndex(destDir, res.Skills); err != nil {
		return res, err
	}
	return res, nil
}

var reSlug = regexp.MustCompile(`[^a-z0-9_-]+`)

// slug turns a folder or skill name into a valid id.
func slug(s string) string {
	s = reSlug.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	s = strings.Trim(s, "-_")
	if len(s) > 64 {
		s = strings.TrimRight(s[:64], "-_")
	}
	return s
}

// readSKILL parses one Agent Skills file: YAML frontmatter with a name and a
// description, then the body that becomes the prompt.
func readSKILL(root *os.Root, p string) (Manifest, string, error) {
	f, err := root.Open(p)
	if err != nil {
		return Manifest{}, "", err
	}
	defer f.Close()
	raw, err := file.ReadCapped(f, maxCatalogFile)
	if errors.Is(err, file.ErrTooLarge) {
		return Manifest{}, "", fmt.Errorf("%s is larger than %d bytes", filepath.Base(p), maxCatalogFile)
	}
	if err != nil {
		return Manifest{}, "", err
	}
	name, desc, body, err := frontmatter(string(raw))
	if err != nil || strings.TrimSpace(body) == "" {
		return Manifest{}, "", fmt.Errorf("%s: %w", p, errors.Join(err, errors.New("no prompt body")))
	}
	id := slug(filepath.Base(filepath.Dir(p)))
	if !reID.MatchString(id) {
		id = slug(name)
	}
	if !reID.MatchString(id) {
		return Manifest{}, "", fmt.Errorf("%s: no usable id", p)
	}
	if name == "" {
		name = id
	}
	if desc == "" {
		desc = name
	}
	m := Manifest{
		ID:               id,
		Name:             text.CutBytes(name, maxName),
		Description:      text.CutBytes(desc, maxDesc),
		Hook:             "system-prompt",
		Routable:         true,
		RoutingMode:      "smart",
		RoutingThreshold: DefaultThreshold,
		Keywords:         deriveKeywords(name + " " + desc),
	}
	if problems := Validate(m); len(problems) > 0 {
		return Manifest{}, "", fmt.Errorf("%s: %s", p, strings.Join(problems, "; "))
	}
	return m, body, nil
}

// frontmatter reads the two keys the registry needs and returns the body. The
// format is hand-parsed: two keys do not justify a YAML module.
func frontmatter(raw string) (name, desc, body string, err error) {
	raw = strings.TrimPrefix(raw, "\ufeff")
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", "", errors.New("SKILL.md has no frontmatter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", "", errors.New("SKILL.md frontmatter is not closed")
	}
	for i := 1; i < end; i++ {
		line := lines[i]
		// Only a key at column 0 is one: a nested map's name or description must
		// not replace the skill's own.
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name", "description":
			v := strings.TrimSpace(value)
			if style, chomp, block := blockStart(v); block {
				i++
				v = blockValue(lines, &i, end, style, chomp)
			} else {
				v = unquote(v)
			}
			if strings.TrimSpace(key) == "name" {
				name = v
			} else {
				desc = v
			}
		}
	}
	return name, desc, strings.TrimSpace(strings.Join(lines[end+1:], "\n")), nil
}

// blockStart reports a YAML block scalar's style and chomping indicator: > or |
// with an optional - or + after it.
func blockStart(v string) (style, chomp byte, ok bool) {
	if v == "" || (v[0] != '>' && v[0] != '|') {
		return 0, 0, false
	}
	switch v[1:] {
	case "":
		return v[0], 0, true
	case "-", "+":
		return v[0], v[1], true
	}
	return 0, 0, false
}

// blockValue reads a block scalar's indented lines from i. Folded (>) joins a
// run of lines with a space and keeps a blank line as a newline; literal (|)
// keeps every newline. The chomp decides the trailing ones.
func blockValue(lines []string, i *int, end int, style, chomp byte) string {
	var raw []string
	for *i < end {
		line := lines[*i]
		if strings.TrimSpace(line) == "" {
			raw = append(raw, "")
			*i++
			continue
		}
		if line[0] != ' ' && line[0] != '\t' {
			break
		}
		raw = append(raw, strings.TrimLeft(line, " \t"))
		*i++
	}
	trail := 0
	for len(raw) > 0 && raw[len(raw)-1] == "" {
		raw = raw[:len(raw)-1]
		trail++
	}
	var s string
	if style == '>' {
		var b strings.Builder
		pending := 0
		for j, l := range raw {
			if l == "" {
				pending++
				continue
			}
			if j > 0 && b.Len() > 0 {
				if pending == 0 {
					b.WriteByte(' ')
				} else {
					b.WriteString(strings.Repeat("\n", pending))
				}
			}
			b.WriteString(l)
			pending = 0
		}
		s = b.String()
	} else {
		s = strings.Join(raw, "\n")
	}
	switch chomp {
	case '-':
	case '+':
		if s != "" {
			s += strings.Repeat("\n", trail+1)
		}
	default:
		if s != "" {
			s += "\n"
		}
	}
	return s
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// deriveKeywords takes the distinctive words of a name and description, since a
// SKILL.md carries no keyword list of its own.
func deriveKeywords(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, term := range relevance.Tokens(s) {
		if len(term) < 4 || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
		if len(out) == 20 {
			break
		}
	}
	return out
}

// writeECC writes one skill unless the destination is the operator's own.
func writeECC(destDir string, m Manifest, prompt string, force bool) (bool, error) {
	dir := filepath.Join(destDir, m.ID)
	if !force {
		old, _, err := LoadDir(dir)
		switch {
		case err == nil && old.Source != sourceECC:
			return false, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return false, nil
		}
	}
	m.Source = sourceECC
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, err
	}
	if err := file.WritePrivate(dir, "manifest.json", raw); err != nil {
		return false, err
	}
	if err := file.WritePrivate(dir, "prompt.md", []byte(prompt)); err != nil {
		return false, err
	}
	return true, nil
}

func writeIndex(destDir string, ids []string) error {
	type listed struct {
		ID string `json:"id"`
	}
	entries := make([]listed, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, listed{ID: id})
	}
	raw, err := json.MarshalIndent(struct {
		Skills []listed `json:"skills"`
	}{entries}, "", "  ")
	if err != nil {
		return err
	}
	return file.WritePrivate(destDir, "_index.json", raw)
}
