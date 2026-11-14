package media

import (
	"errors"
	"strings"

	apptext "rllmx/app/platform/text"
)

// ErrBadCookie means the stored credential is not a cookie header this program
// will send on: empty, or carrying a control character.
var ErrBadCookie = errors.New("media: the stored cookie is not one this program sends")

// pair is one name and value, kept in the order the paste wrote them.
type pair struct {
	Name  string
	Value string
}

// CookieJar is a parsed cookie header.
type CookieJar struct{ pairs []pair }

// ParseCookie reads "name=value; name2=value2". A pair without a name or a value is
// skipped, as a browser would skip it.
func ParseCookie(raw string) (*CookieJar, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || apptext.HasASCIIControl(raw) {
		return nil, ErrBadCookie
	}
	j := &CookieJar{}
	for part := range strings.SplitSeq(raw, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || value == "" {
			continue
		}
		j.pairs = append(j.pairs, pair{Name: name, Value: value})
	}
	if len(j.pairs) == 0 {
		return nil, ErrBadCookie
	}
	return j, nil
}

// Header is the cookie header value, in paste order.
func (j *CookieJar) Header() string {
	parts := make([]string, 0, len(j.pairs))
	for _, p := range j.pairs {
		parts = append(parts, p.Name+"="+p.Value)
	}
	return strings.Join(parts, "; ")
}

// Get returns one cookie's value, "" when it is absent.
func (j *CookieJar) Get(name string) string {
	for _, p := range j.pairs {
		if p.Name == name {
			return p.Value
		}
	}
	return ""
}

// Set replaces a cookie's value in place, or appends it. A bad value is refused
// rather than written.
func (j *CookieJar) Set(name, value string) error {
	if name == "" || value == "" || apptext.HasASCIIControl(name) || apptext.HasASCIIControl(value) || strings.ContainsAny(name, "=; ") {
		return ErrBadCookie
	}
	for i := range j.pairs {
		if j.pairs[i].Name == name {
			j.pairs[i].Value = value
			return nil
		}
	}
	j.pairs = append(j.pairs, pair{Name: name, Value: value})
	return nil
}
