// Package skill holds the skills registry: the manifests an operator imports or
// syncs, the routers that decide which skill a request needs, and the injector
// that places a skill's prompt in the request's system message.
package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"rllmx/app/platform/text"
)

// Bounds on what one skill folder may carry, so a crafted manifest cannot make
// the loader or the request path allocate without limit.
const (
	maxManifest = 32 << 10
	maxPrompt   = 64 << 10
	maxName     = 128
	maxDesc     = 4 << 10
	maxTerms    = 100
	maxTermLen  = 128
	// DefaultThreshold is the score a routable skill must reach when it names
	// no threshold of its own.
	DefaultThreshold = 0.35
)

var (
	reID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// hooks are the row types a manifest may name. Only system-prompt runs in
	// this step; the rest load so a manifest can be inspected, never dispatched.
	hooks = map[string]bool{
		"system-prompt": true, "install-cli": true, "pre-route": true, "pre-request": true, "post-response": true,
	}
)

// Manifest is one skill's metadata. Mode and Threshold decide how the routers
// treat it; Unsupported names what the folder carried but is never run.
type Manifest struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Category         string   `json:"category,omitempty"`
	Hook             string   `json:"hook"`
	DefaultEnabled   bool     `json:"default_enabled,omitempty"`
	LegacyEnabledKey string   `json:"legacy_enabled_key,omitempty"`
	Source           string   `json:"source,omitempty"`
	Routable         bool     `json:"routable,omitempty"`
	RoutingMode      string   `json:"routing_mode,omitempty"`
	RoutingThreshold float64  `json:"routing_threshold,omitempty"`
	Triggers         []string `json:"triggers,omitempty"`
	Keywords         []string `json:"keywords,omitempty"`
	Version          string   `json:"version,omitempty"`
	Config           []Field  `json:"config_schema,omitempty"`
	Unsupported      []string `json:"-"`
}

// wire adds the fields that are read only to be reported as unsupported.
type wire struct {
	Manifest
	Install   string `json:"install_command"`
	Update    string `json:"update_command"`
	Uninstall string `json:"uninstall_command"`
}

// Mode is how the routers treat this skill: "always" injects every request,
// "smart" waits for its topic. A manifest naming neither default is smart, so
// a skill that was never configured cannot quietly change every answer.
func (m Manifest) Mode() string {
	if m.RoutingMode == "smart" || m.RoutingMode == "always" {
		return m.RoutingMode
	}
	for _, f := range m.Config {
		if f.Key != routingModeKey || f.Type != "enum" {
			continue
		}
		if d, msg := CheckValue(f, f.Default); msg == "" && (d == "smart" || d == "always") {
			return d
		}
	}
	return "smart"
}

// Threshold is the score a smart match must reach.
func (m Manifest) Threshold() float64 {
	if m.RoutingThreshold > 0 && m.RoutingThreshold <= 1 {
		return m.RoutingThreshold
	}
	return DefaultThreshold
}

// Validate lists what makes a manifest unusable. A non-empty result means the
// folder is skipped whole, never half-loaded.
func Validate(m Manifest) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if !reID.MatchString(m.ID) {
		add("id %q must match %s", m.ID, reID)
	}
	if s := strings.TrimSpace(m.Name); s == "" || len(s) > maxName || text.HasControl(s) {
		add("name must be 1-%d printable characters", maxName)
	}
	if s := strings.TrimSpace(m.Description); s == "" || len(s) > maxDesc || text.HasControl(s) {
		add("description must be 1-%d printable characters", maxDesc)
	}
	if !hooks[m.Hook] {
		add("hook %q is not system-prompt, install-cli, pre-route, pre-request or post-response", m.Hook)
	}
	if m.RoutingMode != "" && m.RoutingMode != "smart" && m.RoutingMode != "always" {
		add("routing_mode %q must be smart or always", m.RoutingMode)
	}
	if m.RoutingThreshold < 0 || m.RoutingThreshold > 1 {
		add("routing_threshold %v must be between 0 and 1", m.RoutingThreshold)
	}
	for _, list := range []struct {
		name string
		vals []string
	}{{"triggers", m.Triggers}, {"keywords", m.Keywords}} {
		if len(list.vals) > maxTerms {
			add("%s holds %d entries, the cap is %d", list.name, len(list.vals), maxTerms)
		}
		for _, v := range list.vals {
			if v == "" || len(v) > maxTermLen || text.HasControl(v) {
				add("%s entry %q must be 1-%d printable characters", list.name, v, maxTermLen)
				break
			}
		}
	}
	problems = append(problems, checkFields(m.Config)...)
	return problems
}

// LoadDir reads one skill folder on disk.
func LoadDir(dir string) (Manifest, string, error) {
	clean := filepath.Clean(dir)
	m, prompt, _, err := loadFS(os.DirFS(clean), ".", filepath.Base(clean))
	return m, prompt, err
}

// loadFS reads one skill folder through an fs.FS, so the embedded core set and
// an operator's folder take exactly the same checks.
func loadFS(fsys fs.FS, dir, defaultID string) (Manifest, string, []Rule, error) {
	raw, err := readCapped(fsys, path.Join(dir, "manifest.json"), maxManifest)
	if err != nil {
		return Manifest{}, "", nil, err
	}
	var w wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Manifest{}, "", nil, fmt.Errorf("manifest.json: %w", err)
	}
	m := w.Manifest
	if m.ID == "" {
		m.ID = defaultID
	}
	if problems := Validate(m); len(problems) > 0 {
		return Manifest{}, "", nil, fmt.Errorf("%s: %s", m.ID, strings.Join(problems, "; "))
	}
	rules, err := readFolderRules(fsys, dir, m)
	if err != nil {
		return Manifest{}, "", nil, err
	}
	prompt, err := readPrompt(fsys, dir)
	switch {
	case err == nil:
	case len(rules) > 0 && (m.Hook == StagePreRoute || m.Hook == StagePreRequest):
		// A hook that only acts on a rule needs no prompt of its own.
		prompt = ""
	default:
		return Manifest{}, "", nil, err
	}
	m.Unsupported = unsupported(fsys, dir, w)
	return m, prompt, rules, nil
}

// readFolderRules loads a folder's rules file, or nothing when it has none.
func readFolderRules(fsys fs.FS, dir string, m Manifest) ([]Rule, error) {
	raw, err := readCapped(fsys, path.Join(dir, "rules.json"), maxRulesFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return rulesFrom(raw, m.ID, m.Hook)
}

// readPrompt prefers prompt.txt, as the reference registry does, and falls back
// to prompt.md so an imported Agent Skills folder still loads.
func readPrompt(fsys fs.FS, dir string) (string, error) {
	for _, name := range []string{"prompt.txt", "prompt.md"} {
		b, err := readCapped(fsys, path.Join(dir, name), maxPrompt)
		if err == nil {
			if strings.TrimSpace(string(b)) == "" {
				return "", fmt.Errorf("%s is empty", name)
			}
			return string(b), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
	}
	return "", fmt.Errorf("no prompt.txt or prompt.md in %s", dir)
}

// unsupported names what the folder carried that this step will never run.
func unsupported(fsys fs.FS, dir string, w wire) []string {
	var out []string
	for _, c := range []struct{ name, cmd string }{
		{"install_command", w.Install},
		{"update_command", w.Update},
		{"uninstall_command", w.Uninstall},
	} {
		if strings.TrimSpace(c.cmd) != "" {
			out = append(out, c.name)
		}
	}
	if _, err := fs.Stat(fsys, path.Join(dir, "hook.js")); err == nil {
		out = append(out, "hook.js")
	}
	return out
}

// readCapped reads a whole file but refuses one over max, so a skill folder
// cannot make the loader allocate without bound.
func readCapped(fsys fs.FS, name string, max int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", path.Base(name), max)
	}
	return b, nil
}
