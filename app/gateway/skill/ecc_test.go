package skill

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func catalogSource(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		"skills/graphify/SKILL.md": "---\nname: Graphify\ndescription: turn a codebase into a dependency graph\n---\n\n" +
			"Build a graph of the modules and their imports.\n",
		"skills/lint-commit/SKILL.md": "---\nname: Lint Commit\ndescription: check a commit message against house rules\n---\n\n" +
			"Check the message before it is committed.\n",
		"skills/broken/SKILL.md":  "no frontmatter here\n",
		"skills/no-body/SKILL.md": "---\nname: No Body\ndescription: nothing follows\n---\n",
	})
	return src
}

func TestSyncImportsTheCatalog(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "ecc-imported")
	res, err := SyncECC(context.Background(), catalogSource(t), dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 || res.Updated != 2 || res.Skipped != 2 {
		t.Fatalf("result %+v", res)
	}
	if !slices.Equal(res.Skills, []string{"graphify", "lint-commit"}) {
		t.Fatalf("skills %v", res.Skills)
	}
	m, prompt, err := LoadDir(filepath.Join(dest, "graphify"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Source != sourceECC || m.Hook != "system-prompt" || !m.Routable || m.Mode() != "smart" {
		t.Fatalf("manifest %+v", m)
	}
	if len(m.Keywords) == 0 || !strings.Contains(prompt, "Build a graph") {
		t.Fatalf("keywords %v prompt %q", m.Keywords, prompt)
	}
	if _, err := os.Stat(filepath.Join(dest, "_index.json")); err != nil {
		t.Fatalf("index: %v", err)
	}
	if got := NewRegistry(dest, nil).ECCIndex().Score("dependency graph of the modules", 0.2); len(got) == 0 || got[0].ID != "graphify" {
		t.Fatalf("routing after sync %+v", got)
	}
}

func TestSyncLeavesAnEditedSkillAlone(t *testing.T) {
	src := t.TempDir()
	writeFiles(t, src, map[string]string{
		"graphify/SKILL.md": "---\nname: Graphify\ndescription: catalog text\n---\n\nCatalog body.\n",
	})
	dest := filepath.Join(t.TempDir(), "skills")
	if _, err := SyncECC(context.Background(), src, dest, false); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dest, map[string]string{
		"graphify/manifest.json": `{"id":"graphify","name":"Graphify","description":"mine","hook":"system-prompt","routable":true}`,
	})
	res, err := SyncECC(context.Background(), src, dest, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 0 || res.Skipped != 1 {
		t.Fatalf("a hand-written skill was overwritten: %+v", res)
	}
	if m, _, err := LoadDir(filepath.Join(dest, "graphify")); err != nil || m.Description != "mine" {
		t.Fatalf("manifest %+v err %v", m, err)
	}
	res, err = SyncECC(context.Background(), src, dest, true)
	if err != nil || res.Updated != 1 {
		t.Fatalf("force did not update: %+v err %v", res, err)
	}
	if m, _, err := LoadDir(filepath.Join(dest, "graphify")); err != nil || m.Source != sourceECC {
		t.Fatalf("manifest %+v err %v", m, err)
	}
}

func TestSyncRejectsAnEmptySource(t *testing.T) {
	if _, err := SyncECC(context.Background(), "", t.TempDir(), false); err == nil {
		t.Fatal("an empty source was accepted")
	}
	if _, err := SyncECC(context.Background(), t.TempDir(), "", false); err == nil {
		t.Fatal("an empty destination was accepted")
	}
}

func TestSyncStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SyncECC(ctx, catalogSource(t), filepath.Join(t.TempDir(), "out"), false); err == nil {
		t.Fatal("a cancelled sync wrote")
	}
}

func TestFrontmatter(t *testing.T) {
	for name, c := range map[string]struct {
		raw              string
		name, desc, body string
		wantErr          bool
	}{
		"plain": {
			"---\nname: Graphify\ndescription: build a graph\n---\n\nBody text.\n",
			"Graphify", "build a graph", "Body text.", false,
		},
		"quoted": {
			"---\nname: \"Graphify\"\ndescription: 'has: a colon'\n---\nBody.\n",
			"Graphify", "has: a colon", "Body.", false,
		},
		"windows lines and bom": {
			"\ufeff---\r\nname: Graphify\r\ndescription: build a graph\r\n---\r\nBody.\r\n",
			"Graphify", "build a graph", "Body.", false,
		},
		"unclosed": {"---\nname: X\n", "", "", "", true},
		"missing":  {"name: X\n", "", "", "", true},
	} {
		gotName, gotDesc, gotBody, err := frontmatter(c.raw)
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: accepted", name)
			}
			continue
		}
		if err != nil || gotName != c.name || gotDesc != c.desc || gotBody != c.body {
			t.Errorf("%s: %q %q %q %v", name, gotName, gotDesc, gotBody, err)
		}
	}
}

func TestSlug(t *testing.T) {
	for raw, want := range map[string]string{
		"Graphify":              "graphify",
		"Tool Name":             "tool-name",
		"../escape":             "escape",
		"__x__":                 "x",
		strings.Repeat("a", 80): strings.Repeat("a", 64),
	} {
		if got := slug(raw); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}
