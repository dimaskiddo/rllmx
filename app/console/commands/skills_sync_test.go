package commands

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/gateway/skill"
	"rllmx/app/git"
)

const skillManifest = `{"id":"terse","name":"Terse","description":"short answers","hook":"system-prompt","routable":true}`

func sourceTree(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	files := map[string]string{
		"terse/manifest.json": skillManifest,
		"terse/prompt.txt":    "Answer briefly.",
		"plain/manifest.json": `{"id":"plain","name":"Plain","description":"plain text","hook":"system-prompt"}`,
		"plain/prompt.md":     "Write plainly.",
		"unsupported/manifest.json": `{"id":"unsupported","name":"Unsupported","description":"has commands",` +
			`"hook":"system-prompt","install_command":"npm i x"}`,
		"unsupported/prompt.txt": "Text.",
		"unsupported/hook.js":    "process.exit(1)",
		"broken/manifest.json":   `{"id":"Broken","name":"","description":"","hook":"nope"}`,
		"broken/prompt.txt":      "Text.",
	}
	for name, body := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

func TestImportSkillsCopiesWhatItCanUse(t *testing.T) {
	c := &fakeConsole{}
	dataDir := t.TempDir()
	if err := importSkills(c, dataDir, sourceTree(t), "main", nil, false); err != nil {
		t.Fatal(err)
	}
	reg := skill.NewRegistry(filepath.Join(dataDir, "skills"), nil)
	list := reg.List()
	if len(list) != 3 {
		t.Fatalf("skills %+v, problems %v", list, reg.Problems())
	}
	if got := reg.Prompt("plain"); got != "Write plainly." {
		t.Fatalf("prompt.md was not copied: %q", got)
	}
	out := strings.Join(c.lines, "\n")
	if !strings.Contains(out, "unsupported: ignored install_command, hook.js") {
		t.Fatalf("output %q", out)
	}
	if !strings.Contains(out, "skipped") || !strings.Contains(out, "broken") {
		t.Fatalf("the invalid folder was not reported: %q", out)
	}
	if !strings.Contains(out, "3 of 4 copied") {
		t.Fatalf("output %q", out)
	}
}

func TestImportSkillsDryRunCopiesNothing(t *testing.T) {
	c := &fakeConsole{}
	dataDir := t.TempDir()
	if err := importSkills(c, dataDir, sourceTree(t), "main", nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "skills")); err == nil {
		t.Fatal("a dry run wrote the destination")
	}
	if out := strings.Join(c.lines, "\n"); !strings.Contains(out, "would copy terse") {
		t.Fatalf("output %q", out)
	}
}

func TestImportSkillsRefusesAnUnusableSource(t *testing.T) {
	c := &fakeConsole{}
	for name, src := range map[string]string{
		"missing":   filepath.Join(t.TempDir(), "nope"),
		"not https": "http://example.com/skills",
	} {
		if err := importSkills(c, t.TempDir(), src, "main", nil, false); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCatalogSourceUsesALocalFolderAsItIs(t *testing.T) {
	src := t.TempDir()
	dir, cleanup, err := catalogSource(context.Background(), t.TempDir(), src, "main", nil)
	if err != nil || dir != src {
		t.Fatalf("dir %q err %v", dir, err)
	}
	cleanup()
	if _, err := os.Stat(src); err != nil {
		t.Fatal("a local source was removed")
	}
}

func TestCatalogSourceRefusesARemoteSourceWithNoAllowlist(t *testing.T) {
	_, _, err := catalogSource(context.Background(), t.TempDir(), "https://github.com/acme/skills", "main", nil)
	if !errors.Is(err, git.ErrRefused) && !errors.Is(err, git.ErrFailed) {
		t.Fatalf("err %v", err)
	}
}

func TestImportSkillsRefusesALinkToAFileOutsideTheFolder(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "jwt-secret")
	if err := os.WriteFile(secret, []byte("signing secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prompt.txt", "manifest.json", "rules.json"} {
		t.Run(name, func(t *testing.T) {
			src := t.TempDir()
			folder := filepath.Join(src, "terse")
			if err := os.MkdirAll(folder, 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"manifest.json": skillManifest, "prompt.txt": "Answer briefly."}
			delete(files, name)
			for f, body := range files {
				if err := os.WriteFile(filepath.Join(folder, f), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(secret, filepath.Join(folder, name)); err != nil {
				t.Skipf("no symlinks here: %v", err)
			}
			dataDir := t.TempDir()
			c := &fakeConsole{}
			_ = importSkills(c, dataDir, src, "main", nil, false)
			var found bool
			_ = filepath.WalkDir(dataDir, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if b, _ := os.ReadFile(p); strings.Contains(string(b), "signing secret") {
						found = true
					}
				}
				return nil
			})
			if found {
				t.Fatalf("the secret was copied into the data directory (%s)", name)
			}
		})
	}
}
