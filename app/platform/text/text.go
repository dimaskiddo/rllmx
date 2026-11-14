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
