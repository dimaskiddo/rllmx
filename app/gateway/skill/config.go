package skill

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"rllmx/app/platform/text"
)

// What one config_schema field may carry. Every value the dashboard writes goes
// through a prompt, so each is bounded and printable.
const (
	maxFields      = 20
	maxFieldLabel  = 128
	maxFieldOpts   = 50
	maxConfigText  = 256
	activeConfig   = "\n\nActive Configuration:\n"
	routingModeKey = "routing_mode"
)

var (
	reFieldKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	reLegacy   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
	fieldTypes = map[string]bool{"enum": true, "text": true, "number": true, "slider": true, "toggle": true}
)

// Field is one settings control a manifest declares. The dashboard writes its
// value under SettingKey; Apply renders it into the prompt.
type Field struct {
	Key       string          `json:"key"`
	Type      string          `json:"type,omitempty"`
	Label     string          `json:"label,omitempty"`
	Default   json.RawMessage `json:"default,omitempty"`
	LegacyKey string          `json:"legacy_key,omitempty"`
	Min       *float64        `json:"min,omitempty"`
	Max       *float64        `json:"max,omitempty"`
	Options   []Option        `json:"options,omitempty"`
}

// Option is one enum value a field accepts, with the label the dashboard shows.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Desc  string `json:"desc,omitempty"`
}

// SettingKey is the settings row key one field's value lives under. The id is
// part of the default key so two skills naming the same field do not share a
// value; a manifest that names its own legacy key keeps it, as the reference
// does.
func SettingKey(m Manifest, f Field) string {
	if f.LegacyKey != "" {
		return f.LegacyKey
	}
	return m.ID + "_" + f.Key
}

// checkFields lists what makes a manifest's config_schema unusable.
func checkFields(fields []Field) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if len(fields) > maxFields {
		add("config_schema holds %d fields, the cap is %d", len(fields), maxFields)
	}
	seen := map[string]bool{}
	for _, f := range fields {
		if !reFieldKey.MatchString(f.Key) {
			add("config key %q must match %s", f.Key, reFieldKey)
			continue
		}
		if seen[f.Key] {
			add("config key %q is declared twice", f.Key)
		}
		seen[f.Key] = true
		if !fieldTypes[f.Type] {
			add("config key %q has type %q, not enum, text, number, slider or toggle", f.Key, f.Type)
		}
		if f.LegacyKey != "" && !reLegacy.MatchString(f.LegacyKey) {
			add("config key %q has legacy_key %q, which is not a settings key", f.Key, f.LegacyKey)
		}
		if len(f.Label) > maxFieldLabel || text.HasControl(f.Label) {
			add("config key %q has a label over %d bytes or with control characters", f.Key, maxFieldLabel)
		}
		if len(f.Options) > maxFieldOpts {
			add("config key %q has %d options, the cap is %d", f.Key, len(f.Options), maxFieldOpts)
		}
		for _, o := range f.Options {
			if o.Value == "" || text.HasControl(o.Value) || text.HasControl(o.Label) || text.HasControl(o.Desc) {
				add("config key %q has an empty or controlled option", f.Key)
			}
		}
		if len(f.Default) > 0 {
			if _, msg := CheckValue(f, f.Default); msg != "" {
				add("config key %q has default %s: %s", f.Key, f.Default, msg)
			}
		}
	}
	return problems
}

// CheckValue validates one settings value against its field and returns the
// value as the prompt renders it. A refused value comes back with a message and
// is never stored.
func CheckValue(f Field, raw json.RawMessage) (string, string) {
	switch f.Type {
	case "enum", "text":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return "", "must be a string"
		}
		return checkString(f, v)
	case "toggle":
		if s := strings.TrimSpace(string(raw)); s == "true" || s == "false" {
			return s, ""
		}
		return "", "must be true or false"
	case "number", "slider":
		return checkString(f, strings.TrimSpace(string(raw)))
	}
	return "", "has no usable type"
}

// checkString is the same check over the plain text a settings row stores.
func checkString(f Field, v string) (string, string) {
	switch f.Type {
	case "text":
		if len(v) > maxConfigText {
			return "", fmt.Sprintf("must be at most %d bytes", maxConfigText)
		}
		if text.HasControl(v) {
			return "", "must not contain control characters"
		}
		return v, ""
	case "enum":
		for _, o := range f.Options {
			if o.Value == v {
				return v, ""
			}
		}
		return "", "is not one of the field's options"
	case "toggle":
		if v == "true" || v == "false" {
			return v, ""
		}
		return "", "must be true or false"
	case "number", "slider":
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return "", "must be a number"
		}
		if f.Min != nil && n < *f.Min {
			return "", fmt.Sprintf("must be at least %v", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return "", fmt.Sprintf("must be at most %v", *f.Max)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), ""
	}
	return "", "has no usable type"
}

// Render turns a skill's prompt into what the request carries: a {key} takes
// the stored value or the field's default, and a field without a placeholder is
// listed under Active Configuration. A key with neither stays literal, so a
// manifest's own placeholder is never silently emptied.
func Render(m Manifest, prompt string, get func(key string) (string, bool)) string {
	if prompt == "" || len(m.Config) == 0 {
		return prompt
	}
	var lines []string
	for _, f := range m.Config {
		if f.Key == routingModeKey {
			continue
		}
		val, ok := valueOf(m, f, get)
		if !ok {
			continue
		}
		token := "{" + f.Key + "}"
		if strings.Contains(prompt, token) {
			prompt = strings.ReplaceAll(prompt, token, val)
			continue
		}
		label := f.Label
		if label == "" {
			label = f.Key
		}
		if f.Type == "slider" && f.Min != nil && f.Max != nil {
			val += fmt.Sprintf(" (scale %v-%v)", *f.Min, *f.Max)
		}
		lines = append(lines, "- "+label+": "+val)
	}
	if len(lines) == 0 {
		return prompt
	}
	return prompt + activeConfig + strings.Join(lines, "\n")
}

// valueOf is a stored value checked against its field, else the field default.
func valueOf(m Manifest, f Field, get func(key string) (string, bool)) (string, bool) {
	if get != nil {
		if v, ok := get(SettingKey(m, f)); ok {
			if _, msg := checkString(f, v); msg == "" {
				return v, true
			}
		}
	}
	if len(f.Default) == 0 {
		return "", false
	}
	v, msg := CheckValue(f, f.Default)
	return v, msg == ""
}

// FieldFor finds the manifest field one settings key writes, so a PATCH can be
// validated against the field's own rules.
func (r *Registry) FieldFor(key string) (Field, bool) {
	if r == nil {
		return Field{}, false
	}
	for _, m := range r.List() {
		for _, f := range m.Config {
			if SettingKey(m, f) == key {
				return f, true
			}
		}
	}
	return Field{}, false
}

// CheckSetting validates a settings value by the key a manifest gives it.
func (r *Registry) CheckSetting(key string, raw json.RawMessage) (string, string) {
	f, ok := r.FieldFor(key)
	if !ok {
		return "", "is not a setting this router knows"
	}
	return CheckValue(f, raw)
}
