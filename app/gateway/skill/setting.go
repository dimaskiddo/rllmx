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

// Switch resolves a skill's switch and mode: the skill's own key first, the
// legacy key second, then the manifest. get may be nil.
func Switch(m Manifest, get func(string) Setting) (enabled bool, mode string) {
	enabled, mode = m.DefaultEnabled, m.Mode()
	if get == nil {
		return enabled, mode
	}
	if m.LegacyEnabledKey != "" {
		if s := get(m.LegacyEnabledKey); s.HasEnabled {
			enabled = s.Enabled
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
