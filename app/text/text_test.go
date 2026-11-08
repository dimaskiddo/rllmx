package text

import (
	"testing"
	"unicode/utf8"
)

func TestStripControl(t *testing.T) {
	if got := StripControl("a\r\nb\x07é"); got != "abé" {
		t.Fatalf("StripControl = %q", got)
	}
}

func TestCutRunes(t *testing.T) {
	if got := CutRunes("日本語です", 3); got != "日本語" {
		t.Fatalf("got %q", got)
	}
	if got := CutRunes("abc", 3); got != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestCutBytesStaysOnBoundary(t *testing.T) {
	s := "aéééé"
	for n := 0; n <= len(s)+1; n++ {
		got := CutBytes(s, n)
		if len(got) > n || !utf8.ValidString(got) {
			t.Fatalf("n=%d got %q", n, got)
		}
	}
	if CutBytes("abc", 10) != "abc" {
		t.Fatal("short string changed")
	}
}
