package settings

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	envRead    = regexp.MustCompile(`(?:Env|envPath)\("([A-Za-z0-9_]+)"`)
	envAssign  = regexp.MustCompile(`^#?\s*([A-Z][A-Z0-9_]*)=`)
	repoConfig = filepath.Join("..", "..", "..", "config")
	repoEnvEg  = filepath.Join("..", "..", "..", ".env.example")
)

func readByConfig(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(repoConfig, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no config files found: %v", err)
	}
	seen := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range envRead.FindAllStringSubmatch(string(b), -1) {
			seen[m[1]] = true
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func TestConfigReadsExactlyTheKnownNames(t *testing.T) {
	read := readByConfig(t)
	for _, name := range read {
		if !slices.Contains(known, name) {
			t.Errorf("%s is read by config/ but missing from the known list", name)
		}
	}
	for _, name := range known {
		if !slices.Contains(read, name) {
			t.Errorf("%s is in the known list but no config/ file reads it", name)
		}
	}
}

func TestEnvExampleListsExactlyTheKnownNames(t *testing.T) {
	b, err := os.ReadFile(repoEnvEg)
	if err != nil {
		t.Fatal(err)
	}
	inFile := map[string]bool{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if m := envAssign.FindStringSubmatch(line); m != nil {
			inFile[m[1]] = true
		}
	}
	for _, name := range known {
		if !inFile[name] {
			t.Errorf("%s is read by the code but missing from .env.example", name)
		}
	}
	for name := range inFile {
		if !slices.Contains(known, name) {
			t.Errorf("%s is in .env.example but the code does not read it", name)
		}
	}
}

func TestEnvExampleHasNoUnknownNames(t *testing.T) {
	if err := CheckEnvFile(repoEnvEg); err != nil {
		t.Fatal(err)
	}
}

func TestEnvExampleDocumentsTheDollarRule(t *testing.T) {
	b, err := os.ReadFile(repoEnvEg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "single quotes") {
		t.Fatal(".env.example must tell users to single-quote values containing $")
	}
}
