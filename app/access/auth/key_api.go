package auth

import (
	"errors"
	"strings"

	"github.com/tidwall/gjson"

	apptext "rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

// The allow list's bounds, the reference's: a key names at most this many
// models, each at most this long.
const (
	MaxAllowEntries     = 200
	MaxAllowEntryLength = 256
)

// ValidateAccess reads an access object from the dashboard: {restricted,
// allow}. A wrong type, an unexpected field or an entry past its bound is
// refused by name rather than repaired, so a client hears what it got wrong
// instead of finding a list it did not send. A valid one comes back normalized
// the way the store writes it: trimmed, de-duplicated case-insensitively with
// the first spelling kept, and empty when the key is unrestricted.
func ValidateAccess(raw gjson.Result) (repository.KeyAccess, error) {
	if !raw.IsObject() {
		return repository.KeyAccess{}, errors.New("access: must be an object with restricted and allow")
	}
	restricted := raw.Get("restricted")
	if restricted.Type != gjson.True && restricted.Type != gjson.False {
		return repository.KeyAccess{}, errors.New("access.restricted: must be true or false")
	}
	for name := range raw.Map() {
		if name != "restricted" && name != "allow" {
			return repository.KeyAccess{}, errors.New("access: unexpected field " + name)
		}
	}
	list := raw.Get("allow")
	if list.Exists() && !list.IsArray() {
		return repository.KeyAccess{}, errors.New("access.allow: must be a list of model names")
	}
	if list.Exists() && len(list.Array()) > MaxAllowEntries {
		return repository.KeyAccess{}, errors.New("access.allow: too many entries")
	}
	var allow []string
	seen := map[string]bool{}
	for _, entry := range list.Array() {
		if entry.Type != gjson.String {
			return repository.KeyAccess{}, errors.New("access.allow: every entry must be text")
		}
		v := strings.TrimSpace(entry.String())
		if v == "" || seen[strings.ToLower(v)] {
			continue
		}
		if len(v) > MaxAllowEntryLength || apptext.HasControl(v) {
			return repository.KeyAccess{}, errors.New("access.allow: an entry is too long or not text")
		}
		seen[strings.ToLower(v)] = true
		allow = append(allow, v)
	}
	if !restricted.Bool() {
		return repository.KeyAccess{Restricted: false, Allow: []string{}}, nil
	}
	if allow == nil {
		allow = []string{}
	}
	return repository.KeyAccess{Restricted: true, Allow: allow}, nil
}

// MaskKey shows a key's class and enough of its ends to recognise it, never the
// whole value. It is what the dashboard list and keys:list both print.
func MaskKey(key string) string {
	if key == "" {
		return "(unreadable)"
	}
	class := ""
	if i := strings.LastIndex(key, "-"); i >= 0 && i+1 < len(key) {
		class = "…" + key[i+1:]
	}
	if len(key) <= 12 {
		return "…" + key[len(key)/2:] + class
	}
	return key[:5] + "…" + key[len(key)-4:]
}
