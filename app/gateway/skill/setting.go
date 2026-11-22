package skill

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// ReadSetting projects the two settings keys the dashboard writes for one
// skill. get returns a key's JSON text and whether it is stored at all.
func ReadSetting(get func(key string) (string, bool), id string) Setting {
	var s Setting
	if raw, ok := get(id + "Enabled"); ok {
		if v := gjson.Parse(raw); v.Type == gjson.True || v.Type == gjson.False {
			s.Enabled, s.HasEnabled = v.Bool(), true
		}
	}
	if raw, ok := get(id + "RoutingMode"); ok {
		if v := gjson.Parse(raw); v.Type == gjson.String {
			if mode := v.String(); mode == "smart" || mode == "always" {
				s.Mode, s.HasMode = mode, true
			}
		}
	}
	return s
}

// ReadLegacy reads a switch stored under a manifest's own key, which is the
// whole key and not an id that "Enabled" is appended to.
func ReadLegacy(get func(key string) (string, bool), key string) (enabled, ok bool) {
	raw, found := get(key)
	if !found {
		return false, false
	}
	if v := gjson.Parse(raw); v.Type == gjson.True || v.Type == gjson.False {
		return v.Bool(), true
	}
	return false, false
}

// Switch resolves a skill's switch and mode: the skill's own key first, the
// legacy key second, then the manifest. get may be nil, and so may legacy.
func Switch(m Manifest, get func(string) Setting, legacy func(string) (bool, bool)) (enabled bool, mode string) {
	enabled, mode = m.DefaultEnabled, m.Mode()
	if get == nil {
		return enabled, mode
	}
	if m.LegacyEnabledKey != "" && legacy != nil {
		if on, ok := legacy(m.LegacyEnabledKey); ok {
			enabled = on
		}
	}
	if s := get(m.ID); s.HasEnabled || s.HasMode {
		if s.HasEnabled {
			enabled = s.Enabled
		}
		if s.HasMode {
			mode = s.Mode
		}
	}
	return enabled, mode
}

// Sources reads the operator's https allowlist from its stored JSON. A missing
// or unreadable value is empty, which refuses every clone.
func Sources(raw string, found bool) []string {
	if !found {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}

// Readers is the pair of switch readers over one settings document's raw reader:
// the per-skill keys and a manifest's legacy key. Every caller of Switch builds
// them here, so the two cannot be read differently in two places.
func Readers(raw func(key string) (string, bool)) (func(id string) Setting, func(key string) (bool, bool)) {
	return func(id string) Setting { return ReadSetting(raw, id) },
		func(key string) (bool, bool) { return ReadLegacy(raw, key) }
}
