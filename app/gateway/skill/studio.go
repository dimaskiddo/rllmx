package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"rllmx/app/platform/file"
	"rllmx/app/platform/text"
)

// What the Studio refuses, so a caller can tell a missing skill from a folder it
// may not touch.
var (
	ErrExists    = errors.New("a skill with that id exists")
	ErrNotFound  = errors.New("no such skill")
	ErrNotCustom = errors.New("the skill is not a custom one")
	// ErrInvalid wraps every refused draft; its message names the field.
	ErrInvalid = errors.New("invalid skill")
)

// studioHooks are the hooks a skill folder may run. install-cli and
// post-response load but never execute, so the Studio does not offer them.
var studioHooks = map[string]bool{StagePreRoute: true, StagePreRequest: true, "system-prompt": true}

// Draft is what the Studio writes for one custom skill.
type Draft struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	Description      string          `json:"description"`
	Version          string          `json:"version,omitempty"`
	Category         string          `json:"category,omitempty"`
	Hook             string          `json:"hook,omitempty"`
	DefaultEnabled   bool            `json:"default_enabled,omitempty"`
	LegacyEnabledKey string          `json:"legacy_enabled_key,omitempty"`
	Routable         bool            `json:"routable,omitempty"`
	RoutingMode      string          `json:"routing_mode,omitempty"`
	RoutingThreshold float64         `json:"routing_threshold,omitempty"`
	Triggers         []string        `json:"triggers,omitempty"`
	Keywords         []string        `json:"keywords,omitempty"`
	Config           []Field         `json:"config_schema,omitempty"`
	Prompt           string          `json:"prompt_template,omitempty"`
	Rules            json.RawMessage `json:"rules,omitempty"`
}

// RefusedDraft lists the body fields the Studio never accepts: anything that
// would name a process or claim another writer's folder.
var RefusedDraft = []string{"hook.js", "hook_script", "install_command", "update_command", "uninstall_command", "source"}

// manifest renders the draft as the manifest that is written, after every check.
func (d Draft) manifest(id string) (Manifest, []byte, error) {
	m := Manifest{
		ID: id, Name: strings.TrimSpace(d.Name), Description: strings.TrimSpace(d.Description),
		Version: d.Version, Category: d.Category, DefaultEnabled: d.DefaultEnabled,
		LegacyEnabledKey: d.LegacyEnabledKey, Routable: d.Routable, RoutingMode: d.RoutingMode,
		RoutingThreshold: d.RoutingThreshold, Triggers: d.Triggers, Keywords: d.Keywords,
		Config: d.Config, Source: "custom",
	}
	m.Hook = d.Hook
	if m.Hook == "" {
		m.Hook = "system-prompt"
	}
	if !studioHooks[m.Hook] {
		return Manifest{}, nil, fmt.Errorf("%w: hook must be system-prompt, pre-route or pre-request", ErrInvalid)
	}
	if problems := Validate(m); len(problems) > 0 {
		return Manifest{}, nil, fmt.Errorf("%w: %s", ErrInvalid, strings.Join(problems, "; "))
	}
	rules, err := d.ruleBytes(id, m.Hook)
	if err != nil {
		return Manifest{}, nil, err
	}
	if err := d.checkPrompt(m.Hook, len(rules) > 0); err != nil {
		return Manifest{}, nil, err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: the manifest cannot be written", ErrInvalid)
	}
	return m, raw, nil
}

// ruleBytes normalises the draft's rules into the file's own shape and checks
// them against the manifest's hook.
func (d Draft) ruleBytes(id, hook string) ([]byte, error) {
	rules := d.Rules
	if len(rules) == 0 || string(rules) == "null" {
		return nil, nil
	}
	if rules[0] == '[' {
		rules = append(append([]byte(`{"rules":`), rules...), '}')
	}
	if len(rules) > maxRulesFile {
		return nil, fmt.Errorf("%w: rules are over %d bytes", ErrInvalid, maxRulesFile)
	}
	if _, err := rulesFrom(rules, id, hook); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return rules, nil
}

// checkPrompt requires a prompt for a system-prompt skill and allows none for a
// hook that only acts on a rule.
func (d Draft) checkPrompt(hook string, hasRules bool) error {
	prompt := strings.TrimSpace(d.Prompt)
	switch {
	case len(d.Prompt) > maxPrompt:
		return fmt.Errorf("%w: the prompt is over %d bytes", ErrInvalid, maxPrompt)
	case text.HasControl(d.Prompt):
		return fmt.Errorf("%w: the prompt carries control characters", ErrInvalid)
	case hook == "system-prompt" && prompt == "":
		return fmt.Errorf("%w: a system-prompt skill needs prompt_template", ErrInvalid)
	case hook != "system-prompt" && !hasRules:
		return fmt.Errorf("%w: a %s skill needs rules", ErrInvalid, hook)
	}
	return nil
}

// Create writes a new custom skill folder. An id the embedded set already uses,
// the catalog folder, or an existing folder is refused.
func Create(dir string, d Draft) error {
	id := strings.TrimSpace(d.ID)
	if !reID.MatchString(id) {
		return fmt.Errorf("%w: id must match %s", ErrInvalid, reID)
	}
	target, err := skillDir(dir, id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(target); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeSkill(dir, target, id, d, nil)
}

// Update replaces a custom skill's files. A folder this router did not write is
// refused, and a file the draft drops is removed rather than left behind.
func Update(dir, id string, d Draft) error {
	if d.ID != "" && d.ID != id {
		return fmt.Errorf("%w: the body names %q", ErrInvalid, d.ID)
	}
	target, err := skillDir(dir, id)
	if err != nil {
		return err
	}
	if err := checkRealDir(target); err != nil {
		return err
	}
	old, _, err := LoadDir(target)
	if err != nil || old.Source != "custom" {
		return ErrNotCustom
	}
	return writeSkill(dir, target, id, d, &old)
}

// Delete removes a custom skill folder and nothing else.
func Delete(dir, id string) error {
	target, err := skillDir(dir, id)
	if err != nil {
		return err
	}
	if err := checkRealDir(target); err != nil {
		return err
	}
	old, _, err := LoadDir(target)
	if err != nil || old.Source != "custom" {
		return ErrNotCustom
	}
	return os.RemoveAll(target)
}

// checkRealDir requires the folder to exist and to be a real directory, so a
// symlink planted in the skills folder is never written through.
func checkRealDir(target string) error {
	st, err := os.Lstat(target)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case err != nil:
		return err
	case !st.IsDir() || st.Mode()&fs.ModeSymlink != 0:
		return ErrNotCustom
	}
	return nil
}

// Editable reports whether the Studio may write this skill: only a folder whose
// manifest says it is a custom one.
func Editable(m Manifest) bool { return m.Source == "custom" }

// RulesFile reads a skill's rules as the dashboard shaped them, or nil.
func RulesFile(dir, id string) (json.RawMessage, error) {
	target, err := skillDir(dir, id)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer root.Close()
	raw, err := readCapped(root.FS(), "rules.json", maxRulesFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return json.RawMessage(raw), nil
}

// skillDir resolves the one folder an id may name, refusing the catalog folder
// and any id the embedded set already uses.
func skillDir(dir, id string) (string, error) {
	if !reID.MatchString(id) {
		return "", fmt.Errorf("%w: id must match %s", ErrInvalid, reID)
	}
	if id == CatalogDir {
		return "", fmt.Errorf("%w: %s is the catalog folder", ErrInvalid, id)
	}
	if _, err := fs.Stat(Builtin(), path.Join("builtin", id)); err == nil {
		return "", fmt.Errorf("%w: %s is a built-in skill", ErrInvalid, id)
	}
	return filepath.Join(dir, id), nil
}

// writeSkill writes the three files, replacing the ones the draft drops on an
// update.
func writeSkill(dir, target, id string, d Draft, old *Manifest) error {
	_, raw, err := d.manifest(id)
	if err != nil {
		return err
	}
	rules, err := d.ruleBytes(id, draftHook(d))
	if err != nil {
		return err
	}
	if err := file.WritePrivate(target, "manifest.json", raw); err != nil {
		return err
	}
	if strings.TrimSpace(d.Prompt) != "" {
		if err := file.WritePrivate(target, "prompt.txt", []byte(d.Prompt)); err != nil {
			return err
		}
	} else if old != nil {
		if err := removeIfPresent(filepath.Join(target, "prompt.txt")); err != nil {
			return err
		}
	}
	if len(rules) > 0 {
		if err := file.WritePrivate(target, "rules.json", rules); err != nil {
			return err
		}
	} else if old != nil {
		if err := removeIfPresent(filepath.Join(target, "rules.json")); err != nil {
			return err
		}
	}
	return nil
}

func draftHook(d Draft) string {
	if d.Hook == "" {
		return "system-prompt"
	}
	return d.Hook
}

// removeIfPresent deletes a file the draft dropped; absence is success.
func removeIfPresent(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
