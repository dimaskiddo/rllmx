package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const goodManifest = `{"id":"terse","name":"Terse","description":"short answers","hook":"system-prompt",` +
	`"routable":true,"routing_threshold":0.3,"triggers":["be concise"],"keywords":["terse"]}`

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoadDirReadsTheFolder(t *testing.T) {
	t.Run("prompt.txt wins", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"manifest.json": goodManifest,
			"prompt.txt":    "from txt",
			"prompt.md":     "from md",
		})
		m, prompt, err := LoadDir(dir)
		if err != nil || prompt != "from txt" || m.ID != "terse" {
			t.Fatalf("m %+v prompt %q err %v", m, prompt, err)
		}
	})
	t.Run("prompt.md falls back", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"manifest.json": goodManifest, "prompt.md": "from md"})
		if _, prompt, err := LoadDir(dir); err != nil || prompt != "from md" {
			t.Fatalf("prompt %q err %v", prompt, err)
		}
	})
	t.Run("missing prompt", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"manifest.json": goodManifest})
		if _, _, err := LoadDir(dir); err == nil {
			t.Fatal("a folder without a prompt loaded")
		}
	})
	t.Run("missing manifest", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{"prompt.txt": "text"})
		if _, _, err := LoadDir(dir); err == nil {
			t.Fatal("a folder without a manifest loaded")
		}
	})
	t.Run("id defaults to the folder name", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "terse")
		writeFiles(t, dir, map[string]string{
			"manifest.json": `{"name":"Terse","description":"short","hook":"system-prompt"}`,
			"prompt.txt":    "text",
		})
		m, _, err := LoadDir(dir)
		if err != nil || m.ID != "terse" {
			t.Fatalf("id %q err %v", m.ID, err)
		}
	})
}

func TestValidateRefusesBadManifests(t *testing.T) {
	base := Manifest{ID: "terse", Name: "Terse", Description: "short", Hook: "system-prompt"}
	broken := func(mutate func(*Manifest)) Manifest {
		m := base
		mutate(&m)
		return m
	}
	for name, c := range map[string]struct {
		m       Manifest
		wantErr string
	}{
		"good":              {base, ""},
		"parent path id":    {broken(func(m *Manifest) { m.ID = "../x" }), "id"},
		"upper-case id":     {broken(func(m *Manifest) { m.ID = "Terse" }), "id"},
		"long id":           {broken(func(m *Manifest) { m.ID = strings.Repeat("a", 65) }), "id"},
		"empty name":        {broken(func(m *Manifest) { m.Name = " " }), "name"},
		"control in name":   {broken(func(m *Manifest) { m.Name = "a\nb" }), "name"},
		"long description":  {broken(func(m *Manifest) { m.Description = strings.Repeat("d", maxDesc+1) }), "description"},
		"unknown hook":      {broken(func(m *Manifest) { m.Hook = "shell" }), "hook"},
		"unknown mode":      {broken(func(m *Manifest) { m.RoutingMode = "sometimes" }), "routing_mode"},
		"threshold over":    {broken(func(m *Manifest) { m.RoutingThreshold = 1.5 }), "routing_threshold"},
		"threshold under":   {broken(func(m *Manifest) { m.RoutingThreshold = -0.1 }), "routing_threshold"},
		"too many triggers": {broken(func(m *Manifest) { m.Triggers = make([]string, maxTerms+1) }), "triggers"},
		"long trigger":      {broken(func(m *Manifest) { m.Triggers = []string{strings.Repeat("t", maxTermLen+1)} }), "triggers"},
		"empty keyword":     {broken(func(m *Manifest) { m.Keywords = []string{""} }), "keywords"},
	} {
		problems := Validate(c.m)
		got := strings.Join(problems, "; ")
		if c.wantErr == "" && len(problems) != 0 {
			t.Errorf("%s: %s", name, got)
		}
		if c.wantErr != "" && !strings.Contains(got, c.wantErr) {
			t.Errorf("%s: %q does not name %q", name, got, c.wantErr)
		}
	}
}

func TestLoadDirRefusesOversizedFiles(t *testing.T) {
	t.Run("manifest", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"manifest.json": `{"id":"terse","pad":"` + strings.Repeat("x", maxManifest) + `"}`,
			"prompt.txt":    "text",
		})
		if _, _, err := LoadDir(dir); err == nil {
			t.Fatal("an oversized manifest loaded")
		}
	})
	t.Run("prompt", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"manifest.json": goodManifest,
			"prompt.txt":    strings.Repeat("x", maxPrompt+1),
		})
		if _, _, err := LoadDir(dir); err == nil {
			t.Fatal("an oversized prompt loaded")
		}
	})
}

func TestUnsupportedNamesWhatWillNotRun(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"manifest.json": `{"id":"terse","name":"Terse","description":"short","hook":"system-prompt",` +
			`"install_command":"npm i x","update_command":"git pull","hook.js":"never run"}`,
		"prompt.txt": "text",
		"hook.js":    "process.exit(1)",
	})
	m, _, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"install_command": true, "update_command": true, "hook.js": true}
	for _, u := range m.Unsupported {
		delete(want, u)
	}
	if len(want) != 0 {
		t.Fatalf("unsupported %v does not name %v", m.Unsupported, want)
	}
}

func TestRegistrySkipsBrokenFoldersAndLetsTheDiskWin(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"terse/manifest.json": goodManifest,
		"terse/prompt.txt":    "disk text",
		"bad/manifest.json":   `{"id":"bad","name":"","description":"","hook":"nope"}`,
		"bad/prompt.txt":      "text",
	})
	builtins := fstest.MapFS{
		"builtin/terse/manifest.json": {Data: []byte(goodManifest)},
		"builtin/terse/prompt.txt":    {Data: []byte("builtin text")},
	}
	r := NewRegistry(root, builtins)
	list := r.List()
	if len(list) != 1 || list[0].ID != "terse" {
		t.Fatalf("list %+v", list)
	}
	if got := r.Prompt("terse"); got != "disk text" {
		t.Fatalf("prompt %q", got)
	}
	if problems := r.Problems(); len(problems) != 1 || !strings.Contains(problems[0], "bad") {
		t.Fatalf("problems %v", problems)
	}
	if r.Signature() == "" {
		t.Fatal("no signature")
	}
}

func TestRegistryReloadsWhenAPromptChanges(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"terse/manifest.json": goodManifest,
		"terse/prompt.txt":    "one",
	})
	r := NewRegistry(root, nil)
	if got := r.Prompt("terse"); got != "one" {
		t.Fatalf("prompt %q", got)
	}
	writeFiles(t, root, map[string]string{"terse/prompt.txt": "a longer second text"})
	if got := r.Prompt("terse"); got != "a longer second text" {
		t.Fatalf("prompt after rewrite %q", got)
	}
	if len(r.List()) != 1 {
		t.Fatal("the skill disappeared after reload")
	}
}

func TestManifestModeAndThreshold(t *testing.T) {
	for name, c := range map[string]struct {
		m      Manifest
		mode   string
		thresh float64
	}{
		"defaults":         {Manifest{}, "smart", DefaultThreshold},
		"top-level always": {Manifest{RoutingMode: "always"}, "always", DefaultThreshold},
		"schema default": {Manifest{Config: []Field{{Key: routingModeKey, Type: "enum",
			Default: []byte(`"always"`), Options: []Option{{Value: "smart"}, {Value: "always"}}}}}, "always", DefaultThreshold},
		"schema ignores typo": {Manifest{Config: []Field{{Key: routingModeKey, Type: "enum",
			Default: []byte(`"sometimes"`), Options: []Option{{Value: "smart"}, {Value: "always"}}}}}, "smart", DefaultThreshold},
		"own threshold": {Manifest{RoutingThreshold: 0.2}, "smart", 0.2},
	} {
		if got := c.m.Mode(); got != c.mode {
			t.Errorf("%s: mode %q, want %q", name, got, c.mode)
		}
		if got := c.m.Threshold(); got != c.thresh {
			t.Errorf("%s: threshold %v, want %v", name, got, c.thresh)
		}
	}
}

func TestTheEmbeddedCoreSkillsLoad(t *testing.T) {
	r := NewRegistry("", Builtin())
	want := []string{"caveman-style", "code-humanizer", "commit-lint", "ponytail-style", "prose-humanizer", "watermarks-remover"}
	list := r.List()
	if len(list) != len(want) {
		t.Fatalf("list %+v", list)
	}
	for i, m := range list {
		if m.ID != want[i] {
			t.Errorf("skill %d is %q, want %q", i, m.ID, want[i])
		}
		if m.Hook != "system-prompt" {
			t.Errorf("%s hook %q", m.ID, m.Hook)
		}
		if !m.Routable || (m.Mode() != "smart" && m.Mode() != "always") {
			t.Errorf("%s is not routable", m.ID)
		}
		if m.Threshold() < 0.25 || m.Threshold() > 0.5 {
			t.Errorf("%s threshold %v", m.ID, m.Threshold())
		}
		if prompt := r.Prompt(m.ID); len(prompt) < 64 || len(prompt) > 32<<10 {
			t.Errorf("%s prompt is %d bytes", m.ID, len(prompt))
		}
		if len(m.Unsupported) != 0 {
			t.Errorf("%s carries %v", m.ID, m.Unsupported)
		}
	}
	if problems := r.Problems(); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
}

func TestANilRegistryIsQuiet(t *testing.T) {
	var r *Registry
	if r.List() != nil || r.Prompt("x") != "" || r.Signature() != "" || r.Problems() != nil {
		t.Fatal("a nil registry answered")
	}
}
