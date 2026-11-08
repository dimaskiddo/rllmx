// Package text holds the small string helpers several packages need: control
// characters removed from anything logged, and cuts that never split a
// character.
package text

import (
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
