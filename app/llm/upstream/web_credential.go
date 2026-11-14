package upstream

import (
	"encoding/json"
	"rllmx/app/platform/text"
	"strings"
)

// MaxWebCredential is the largest pasted credential this router stores: the
// same bound the connection create route applies to any secret.
const MaxWebCredential = 16 << 10

// ParseWebCredential reads what a user pastes as a web provider's credential
// and returns it normalized: a bare token as it stands, or a `name=value; …`
// header in the order it was pasted. It accepts a bare value, one or more
// `name=value` pairs (with or without a `Cookie:` prefix), a `Bearer …` value,
// a cookie-editor JSON array, and the `{"value": …}` object some of these
// vendors keep in local storage. The second result is false when the text is
// not a credential this router will store: it is empty, too long, holds a
// control character, or a pair's name or value is not shaped the way a request
// header can carry.
func ParseWebCredential(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > MaxWebCredential || text.HasControl(s) {
		return "", false
	}
	if v, ok := strings.CutPrefix(s, "Bearer "); ok {
		return bareCredential(strings.TrimSpace(v))
	}
	if v, ok := strings.CutPrefix(s, "Cookie: "); ok {
		s = strings.TrimSpace(v)
	} else if v, ok := strings.CutPrefix(s, "cookie: "); ok {
		s = strings.TrimSpace(v)
	}
	if strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
		return jsonCredential(s)
	}
	if strings.Contains(s, "=") {
		return cookiePairs(s)
	}
	return bareCredential(s)
}

// CookieName reports whether s can be the name half of a cookie pair: letters,
// digits, underscore, dash and dot, at most 128 bytes.
func CookieName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// CookieValue reports whether s can be the value half of a cookie pair: no
// whitespace and none of the characters that would change the shape of the
// header it is written into.
func CookieValue(s string) bool {
	if s == "" {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return r <= ' ' || r == '"' || r == ',' || r == ';' || r == 0x7f
	}) < 0
}

// bareCredential validates a value with no name around it.
func bareCredential(s string) (string, bool) {
	if s == "" || text.HasControl(s) || strings.ContainsAny(s, " \t\";,") {
		return "", false
	}
	return s, true
}

// cookiePairs reads `name=value; name2=value2`, keeping the pasted order.
func cookiePairs(s string) (string, bool) {
	pairs := strings.Split(s, ";")
	out := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue // a trailing semicolon
		}
		name, value, found := strings.Cut(pair, "=")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !found || !CookieName(name) || !CookieValue(value) {
			return "", false
		}
		out = append(out, name+"="+value)
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, "; "), true
}

// cookieEntry is one entry of a Cookie-Editor export.
type cookieEntry struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// cookiesOf joins checked entries into one Cookie header value, keeping order.
func cookiesOf(entries []cookieEntry) (string, bool) {
	if len(entries) == 0 {
		return "", false
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !CookieName(e.Name) || !CookieValue(e.Value) {
			return "", false
		}
		out = append(out, e.Name+"="+e.Value)
	}
	return strings.Join(out, "; "), true
}

// jsonCredential reads the shapes a pasted credential arrives in: a
// cookie-editor array of {name, value} entries, a {"cookies":[…]} export, and a
// local-storage object holding the token under "value".
func jsonCredential(s string) (string, bool) {
	if strings.HasPrefix(s, "{") {
		var obj struct {
			Value   string        `json:"value"`
			Cookies []cookieEntry `json:"cookies"`
		}
		if json.Unmarshal([]byte(s), &obj) != nil {
			return "", false
		}
		if obj.Cookies != nil {
			return cookiesOf(obj.Cookies)
		}
		return bareCredential(strings.TrimSpace(obj.Value))
	}
	var entries []cookieEntry
	if json.Unmarshal([]byte(s), &entries) != nil {
		return "", false
	}
	return cookiesOf(entries)
}
