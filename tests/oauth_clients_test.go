package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The public client ids of the tools whose logins the router uses live in
// configuration only. They are held here as hashes, so this file
// does not carry the literals it forbids elsewhere.
var clientIDHashes = map[string]string{
	"claude": "473668f2b13c71009d028ff0ef74c2cf76e71cbdd33b76e69fcc42d7e59aca4b",
	"codex":  "584341c2f0e88ad1f7c6856553d81dc4776ff42c43951daed3e2d8d91552eaa2",
	"github": "9954bfea80f3d9092fcfed1cf263006469bc818ce55ee7114580fe2a8142af71",
}

var scanRoots = []string{"app", "bootstrap", "config", "routes", "pkg", "web"}

func isIDChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

// findClientIDs returns "path: name" for every file under the roots that holds
// a client id as a whole word, and how many files it read.
func findClientIDs(t *testing.T, roots []string) (hits []string, files int) {
	t.Helper()
	wanted := map[string]string{}
	for name, h := range clientIDHashes {
		wanted[h] = name
	}
	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files++
			for _, word := range strings.FieldsFunc(string(raw), func(r rune) bool { return !isIDChar(r) }) {
				if len(word) < 20 || len(word) > 64 {
					continue
				}
				sum := sha256.Sum256([]byte(word))
				if name, hit := wanted[hex.EncodeToString(sum[:])]; hit {
					hits = append(hits, filepath.ToSlash(path)+": "+name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return hits, files
}

func TestNoClientIDIsWrittenInCodeDataOrWebFiles(t *testing.T) {
	roots := make([]string, 0, len(scanRoots))
	for _, r := range scanRoots {
		roots = append(roots, filepath.Join("..", r))
	}
	hits, files := findClientIDs(t, roots)
	for _, h := range hits {
		t.Errorf("%s client id; it belongs in configuration", h)
	}
	if files < 100 {
		t.Fatalf("only %d files were scanned", files)
	}
}

func TestTheScanFindsAnIDThatIsPlanted(t *testing.T) {
	dir := t.TempDir()
	id := "9d1c250a-e61b-44d9-" + "88ed-5944d1962f5e"
	if err := os.WriteFile(filepath.Join(dir, "x.json"), []byte(`{"clientId":"`+id+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "y.js"), []byte(`const near = "`+id+`-not"`), 0o600); err != nil {
		t.Fatal(err)
	}
	hits, files := findClientIDs(t, []string{dir})
	if files != 2 || len(hits) != 1 || !strings.HasSuffix(hits[0], "x.json: claude") {
		t.Fatalf("%d files, hits %v", files, hits)
	}
}
