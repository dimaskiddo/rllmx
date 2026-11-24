// Package text holds the small string helpers several packages need: control
// characters removed from anything logged, and cuts that never split a
// character.
package text

import (
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// StripControl removes control characters.
func StripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

// CutRunes keeps at most n characters.
func CutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// CutBytes keeps at most n bytes, backing up to a character boundary.
func CutBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var (
	markup = regexp.MustCompile(`<[^>]*>`)
	spaces = regexp.MustCompile(`\s+`)
)

// StripMarkup turns text that may be an HTML page into one short plain line: tags
// removed (a second time after entities are decoded, so an escaped tag goes
// too), control characters dropped, white space collapsed to single spaces, and
// the result cut to at most max bytes on a character boundary.
func StripMarkup(s string, max int) string {
	s = markup.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = markup.ReplaceAllString(s, " ")
	s = StripControl(s)
	s = spaces.ReplaceAllString(s, " ")
	return strings.ToValidUTF8(CutBytes(strings.TrimSpace(s), max), "")
}

// HasASCIIControl reports a byte below U+0020 or DEL. Header values and data
// file strings use this narrower rule: a line break or a NUL changes what a
// header means, and C1 characters in a name are the file author's business.
func HasASCIIControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// HasControl reports any control character, C1 included: no header value, name
// or secret may carry one.
func HasControl(s string) bool { return strings.ContainsFunc(s, unicode.IsControl) }

// DataLine reads one line of an event stream or of plain lines: it trims the
// space around it and a leading "data:" field name if there is one. had says
// whether the name was there, for a caller that only accepts a framed line.
func DataLine(line string) (payload string, had bool) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	rest, had := strings.CutPrefix(line, "data:")
	return strings.TrimSpace(rest), had
}

// FirstNonEmpty returns the first of vals that holds something other than
// space, or "".
func FirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

const pathSpecials = `.*?#|@\:!=<>%"`

// EscapePath makes a name one literal segment of a gjson or sjson path, so a dot
// or a wildcard in it is part of the name and not a step.
func EscapePath(name string) string {
	if !strings.ContainsAny(name, pathSpecials) {
		return name
	}
	var b strings.Builder
	b.Grow(len(name) + 4)
	for i := 0; i < len(name); i++ {
		if strings.IndexByte(pathSpecials, name[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(name[i])
	}
	return b.String()
}

// IsInvisible reports a character an AI provenance watermark uses: a zero-width
// space, joiner or word joiner, the BOM, a soft hyphen, or a bidi control.
func IsInvisible(r rune) bool {
	switch {
	case r == 0x00ad, r == 0x200b, r == 0x200c, r == 0x200d, r == 0x2060, r == 0xfeff:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// StripInvisible removes the invisible characters above from a body, as raw
// UTF-8 and as the JSON \uXXXX escapes a wire body carries. An escaped backslash
// that merely spells one of those escapes is text a reader can see, so it stays.
// The answer is the input itself when nothing was removed.
func StripInvisible(b []byte) []byte {
	out := b[:0]
	changed := false
	for i := 0; i < len(b); {
		if b[i] == '\\' && i+1 < len(b) {
			if b[i+1] == '\\' {
				out = append(out, b[i], b[i+1])
				i += 2
				continue
			}
			if size, ok := escapedInvisible(b[i:]); ok {
				i += size
				changed = true
				continue
			}
		}
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError && IsInvisible(r) {
			i += size
			changed = true
			continue
		}
		out = append(out, b[i:i+size]...)
		i += size
	}
	if !changed {
		return b
	}
	return out
}

// escapedInvisible reads a \uXXXX escape of four hex digits at the start of b
// and reports its size when the character it names is invisible.
func escapedInvisible(b []byte) (int, bool) {
	if len(b) < 6 || b[0] != '\\' || b[1] != 'u' {
		return 0, false
	}
	var r rune
	for _, c := range b[2:6] {
		switch {
		case c >= '0' && c <= '9':
			r = r*16 + rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r*16 + rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r*16 + rune(c-'A'+10)
		default:
			return 0, false
		}
	}
	return 6, IsInvisible(r)
}
