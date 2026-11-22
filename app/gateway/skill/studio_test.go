package skill

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func customDraft() Draft {
	return Draft{ID: "mine", Name: "Mine", Description: "a custom skill", Hook: "system-prompt",
		Prompt: "CUSTOM PROMPT", Triggers: []string{"custom thing"}, Routable: true}
}

func TestCreateWritesACustomSkill(t *testing.T) {
	dir := t.TempDir()
	if err := Create(dir, customDraft()); err != nil {
		t.Fatal(err)
	}
	m, prompt, err := LoadDir(filepath.Join(dir, "mine"))
	if err != nil || prompt != "CUSTOM PROMPT" || m.Source != "custom" || !Editable(m) {
		t.Fatalf("m %+v prompt %q err %v", m, prompt, err)
	}
	if r := NewRegistry(dir, nil); len(r.List()) != 1 || r.Problems() != nil {
		t.Fatalf("list %+v problems %v", r.List(), r.Problems())
	}
	if err := Create(dir, customDraft()); !errors.Is(err, ErrExists) {
		t.Fatalf("a second create: %v", err)
	}
}

func TestCreateRefusesWhereItMayNotWrite(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"ecc-imported/x/manifest.json": `{"id":"x","name":"X","description":"d","hook":"system-prompt","source":"ecc"}`,
		"ecc-imported/x/prompt.md":     "text",
	})
	for name, id := range map[string]string{
		"parent path": "../escape",
		"catalog":     CatalogDir,
		"built-in":    "caveman-style",
		"upper case":  "Mine",
	} {
		d := customDraft()
		d.ID = id
		if err := Create(dir, d); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := Create(dir, Draft{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an empty id: %v", err)
	}
}

func TestDraftChecksTheHookAndPrompt(t *testing.T) {
	for name, c := range map[string]struct {
		mut func(*Draft)
		bad string
	}{
		"install hook": {func(d *Draft) { d.Hook = "install-cli" }, "hook"},
		"no prompt":    {func(d *Draft) { d.Prompt = "" }, "prompt_template"},
		"controlled":   {func(d *Draft) { d.Prompt = "a\nb" }, "control"},
		"bad rule": {func(d *Draft) {
			d.Hook = "pre-route"
			d.Prompt = ""
			d.Rules = []byte(`[{"stage":"pre-route","when":{},"then":{"rerouteModel":"x"}}]`)
		}, "when"},
		"rule for hook": {func(d *Draft) {
			d.Rules = []byte(`[{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"y"}}]`)
		}, "hook"},
		"wrong stage":    {func(d *Draft) { d.Prompt = ""; d.Hook = "pre-request" }, "rules"},
		"bad default":    {func(d *Draft) { d.Config = []Field{{Key: "level", Type: "enum", Default: []byte(`"x"`)}} }, "default"},
		"long prompt":    {func(d *Draft) { d.Prompt = strings.Repeat("p", maxPrompt+1) }, "over"},
		"unknown config": {func(d *Draft) { d.Config = []Field{{Key: "Level", Type: "text"}} }, "config key"},
	} {
		d := customDraft()
		c.mut(&d)
		err := Create(t.TempDir(), d)
		if err == nil || !strings.Contains(err.Error(), c.bad) {
			t.Errorf("%s: %v (want %q)", name, err, c.bad)
		}
	}
}

func TestAHookSkillNeedsOnlyItsRules(t *testing.T) {
	dir := t.TempDir()
	d := Draft{ID: "router", Name: "Router", Description: "routes", Hook: "pre-route",
		Rules: []byte(`[{"stage":"pre-route","when":{"modelRegex":"^fast-"},"then":{"rerouteModel":"slow"}}]`)}
	if err := Create(dir, d); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry(dir, nil)
	if got := r.Rules(StagePreRoute, nil); len(got) != 1 || got[0].ID != "router" {
		t.Fatalf("rules %+v", got)
	}
	if raw, err := RulesFile(dir, "router"); err != nil || !strings.Contains(string(raw), "rerouteModel") {
		t.Fatalf("rules file %s err %v", raw, err)
	}
}

func TestUpdateRewritesOnlyACustomFolder(t *testing.T) {
	dir := t.TempDir()
	if err := Create(dir, customDraft()); err != nil {
		t.Fatal(err)
	}
	d := customDraft()
	d.Name = "Mine Again"
	d.Prompt = "SECOND PROMPT"
	if err := Update(dir, "mine", d); err != nil {
		t.Fatal(err)
	}
	m, prompt, err := LoadDir(filepath.Join(dir, "mine"))
	if err != nil || m.Name != "Mine Again" || prompt != "SECOND PROMPT" {
		t.Fatalf("m %+v prompt %q err %v", m, prompt, err)
	}

	writeFiles(t, dir, map[string]string{
		"hand/manifest.json":             `{"id":"hand","name":"Hand","description":"placed by hand","hook":"system-prompt"}`,
		"hand/prompt.txt":                "text",
		"ecc-imported/imp/manifest.json": `{"id":"imp","name":"Imp","description":"imported","hook":"system-prompt","source":"ecc"}`,
		"ecc-imported/imp/prompt.md":     "text",
	})
	named := func(id string) Draft { d := customDraft(); d.ID = id; return d }
	if err := Update(dir, "hand", named("hand")); !errors.Is(err, ErrNotCustom) {
		t.Fatalf("hand-written: %v", err)
	}
	// An imported skill's folder is nested under the catalog folder, so the
	// Studio never addresses it; it is not found and left alone.
	if err := Update(dir, "imp", named("imp")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("imported: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ecc-imported", "imp", "prompt.md")); err != nil {
		t.Fatal("a refused update touched the imported folder")
	}
	if err := Update(dir, "missing", named("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := Update(dir, "caveman-style", named("caveman-style")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("built-in: %v", err)
	}
	if err := Update(dir, "mine", Draft{ID: "other", Name: "X", Description: "d", Prompt: "p"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a body naming another id: %v", err)
	}
}

func TestUpdateRemovesFilesTheDraftDrops(t *testing.T) {
	dir := t.TempDir()
	d := Draft{ID: "swap", Name: "Swap", Description: "d", Hook: "pre-request",
		Rules: []byte(`[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"injectPrompt":"p"}}]`)}
	if err := Create(dir, d); err != nil {
		t.Fatal(err)
	}
	d2 := Draft{ID: "swap", Name: "Swap", Description: "d", Hook: "system-prompt", Prompt: "now a prompt"}
	if err := Update(dir, "swap", d2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "swap", "rules.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the removed rules are still there: %v", err)
	}
	if _, prompt, err := LoadDir(filepath.Join(dir, "swap")); err != nil || prompt != "now a prompt" {
		t.Fatalf("prompt %q err %v", prompt, err)
	}
}

func TestDeleteRefusesWhatItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	if err := Create(dir, customDraft()); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{
		"hand/manifest.json": `{"id":"hand","name":"Hand","description":"placed by hand","hook":"system-prompt"}`,
		"hand/prompt.txt":    "text",
	})
	if err := Delete(dir, "hand"); !errors.Is(err, ErrNotCustom) {
		t.Fatalf("hand-written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hand")); err != nil {
		t.Fatal("a refused delete removed the folder")
	}
	if err := Delete(dir, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := Delete(dir, CatalogDir); !errors.Is(err, ErrInvalid) {
		t.Fatalf("the catalog folder: %v", err)
	}
	if err := Delete(dir, "mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "mine")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the folder survived its delete")
	}
}

func TestASymlinkIsNeverWrittenThrough(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(dir, "link")); err != nil {
		t.Skip("symlinks are not available here")
	}
	d := customDraft()
	d.ID = "link"
	if err := Update(dir, "link", d); !errors.Is(err, ErrNotCustom) {
		t.Fatalf("update: %v", err)
	}
	if err := Delete(dir, "link"); !errors.Is(err, ErrNotCustom) {
		t.Fatalf("delete: %v", err)
	}
	if entries, err := os.ReadDir(elsewhere); err != nil || len(entries) != 0 {
		t.Fatalf("the link's target changed: %v %v", entries, err)
	}
}

func TestDraftTriggersAndKeywordsAreKept(t *testing.T) {
	dir := t.TempDir()
	d := customDraft()
	d.Triggers = []string{"one", "two"}
	d.Keywords = []string{"three"}
	if err := Create(dir, d); err != nil {
		t.Fatal(err)
	}
	m, _, err := LoadDir(filepath.Join(dir, "mine"))
	if err != nil || !slices.Equal(m.Triggers, []string{"one", "two"}) || !slices.Equal(m.Keywords, []string{"three"}) {
		t.Fatalf("m %+v err %v", m, err)
	}
}
