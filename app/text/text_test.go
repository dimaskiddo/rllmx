package text

import (
	"strings"
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

func TestStripMarkupLeavesOnePlainLine(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"plain text", 100, "plain text"},
		{"<html><body><h1>502 Bad Gateway</h1></body></html>", 100, "502 Bad Gateway"},
		{"a &lt;script&gt;alert(1)&lt;/script&gt; b", 100, "a alert(1) b"},
		{"line one \r\nline two \n\n three", 100, "line one line two three"},
		{"a\tb\nc", 100, "abc"},
		{"bell\x07 and nul\x00 gone", 100, "bell and nul gone"},
		{"c1 \u0085 \u009f control", 100, "c1 control"},
		{"  padded  ", 100, "padded"},
		{"héllo wörld", 7, "héllo "},
		{"日本語のテキスト", 7, "日本"},
		{"bad \xff\xfe utf8", 100, "bad �� utf8"},
		{"", 10, ""},
		{"<a><b></b></a>", 10, ""},
	} {
		if got := StripMarkup(c.in, c.max); got != c.want {
			t.Errorf("%q (max %d): got %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestStripMarkupNeverReturnsATagOrMoreThanMax(t *testing.T) {
	for _, in := range []string{
		"<<b>script>x", "&lt;&lt;b&gt;script&gt;", "&amp;lt;b&amp;gt;", strings.Repeat("<p>é</p>", 500), "<" + strings.Repeat("a", 1000),
	} {
		got := StripMarkup(in, 64)
		if len(got) > 64 || !utf8.ValidString(got) || strings.ContainsAny(got, "\r\n\t") {
			t.Errorf("%.20q: %q (%d bytes)", in, got, len(got))
		}
		if markup.MatchString(got) {
			t.Errorf("%.20q left a tag: %q", in, got)
		}
	}
}

func FuzzStripMarkup(f *testing.F) {
	for _, s := range []string{"", "<b>x</b>", "&lt;i&gt;", "\x00\xff", "é<"} {
		f.Add(s, 20)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 0 || max > 4096 {
			return
		}
		got := StripMarkup(s, max)
		if len(got) > max || !utf8.ValidString(got) || markup.MatchString(got) {
			t.Fatalf("%q max %d: %q", s, max, got)
		}
	})
}

func TestHasASCIIControlIsTheNarrowRule(t *testing.T) {
	for in, want := range map[string]bool{
		"": false, "plain": false, "tab\there": true, "nl\n": true, "cr\r": true, "nul\x00": true, "del\x7f": true,
		"c1\u0085": false, "é日本": false, "space and ~": false,
	} {
		if got := HasASCIIControl(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
