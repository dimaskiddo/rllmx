package text

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestAnEscapedNameIsOneLiteralPathSegment(t *testing.T) {
	for _, name := range []string{"plain", "a.b", "a*b", "a?b", "a#b", "a|b", "a@b", `a\b`, "a:b", "a!b", "a=b", "a<b", "a>b", "a%b", `a"b`, "gpt-4.1.mini", "é.ü"} {
		body, err := sjson.Set("{}", EscapePath(name), 1)
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if got := gjson.Get(body, EscapePath(name)); got.Int() != 1 {
			t.Errorf("%q: read back %v from %s", name, got, body)
		}
		keys := gjson.Parse(body).Map()
		if _, ok := keys[name]; !ok || len(keys) != 1 {
			t.Errorf("%q is not the one key in %s", name, body)
		}
	}
}

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

func TestDataLineTrimsTheFieldNameAndSaysWhetherItWasThere(t *testing.T) {
	for name, tc := range map[string]struct {
		in      string
		payload string
		had     bool
	}{
		"framed":          {"data: {\"a\":1}", `{"a":1}`, true},
		"framed, no gap":  {"data:{\"a\":1}", `{"a":1}`, true},
		"bare":            {`{"a":1}`, `{"a":1}`, false},
		"carriage return": {"data: x\r", "x", true},
		"padded":          {"  data:  x  ", "x", true},
		"another field":   {"event: done", "event: done", false},
		"empty":           {"", "", false},
	} {
		if got, had := DataLine(tc.in); got != tc.payload || had != tc.had {
			t.Errorf("%s: (%q, %v), want (%q, %v)", name, got, had, tc.payload, tc.had)
		}
	}
}

func TestFirstNonEmptySkipsBlanks(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "a", "b"); got != "a" {
		t.Fatalf("%q", got)
	}
	if got := FirstNonEmpty("", " "); got != "" {
		t.Fatalf("%q", got)
	}
}

func TestAControlCharacterIsAnyC0DelOrC1(t *testing.T) {
	for in, want := range map[string]bool{
		"plain": false, "tab\there": true, "line\nbreak": true, "nul\x00": true,
		"del\x7f": true, "c1\u0085": true, "c1\u009f": true, "latin é": false, "": false,
	} {
		if got := HasControl(in); got != want {
			t.Errorf("HasControl(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStripInvisibleRemovesEveryWatermarkCharacter(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"a" + string(rune(0x200b)) + "b", "ab"},
		{"a" + string(rune(0x200c)) + string(rune(0x200d)) + "b", "ab"},
		{"a" + string(rune(0x2060)) + "b", "ab"},
		{"a" + string(rune(0xfeff)) + "b", "ab"},
		{"soft" + string(rune(0x00ad)) + "hyphen", "softhyphen"},
		{"rtl" + string(rune(0x202e)) + "evil", "rtlevil"},
		{"iso" + string(rune(0x2066)) + "late" + string(rune(0x2069)), "isolate"},
		{`{"t":"a\u200bb"}`, `{"t":"ab"}`},
		{`{"t":"\ufeffx\u2069"}`, `{"t":"x"}`},
		{`{"t":"a\\u200bb"}`, `{"t":"a\\u200bb"}`},
		{`{"t":"\\\u200b"}`, `{"t":"\\"}`},
		{`{"t":"\u00e9 and \u200b"}`, `{"t":"\u00e9 and "}`},
	}
	for _, tc := range cases {
		got := string(StripInvisible([]byte(tc.in)))
		if got != tc.want {
			t.Errorf("StripInvisible(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The input slice is handed back untouched when there was nothing to remove, so
// a response with no mark is forwarded byte for byte.
func TestStripInvisibleAllocatesNothingForACleanBody(t *testing.T) {
	in := []byte(`{"t":"clean"}`)
	out := StripInvisible(in)
	if &out[0] != &in[0] {
		t.Fatal("a clean body was copied")
	}
}

func FuzzStripInvisible(f *testing.F) {
	f.Add([]byte(`{"t":"ab"}`))
	f.Add([]byte(`{"t":"a\\u200bb"}`))
	f.Add([]byte("a" + string(rune(0x200b)) + "b"))
	f.Fuzz(func(t *testing.T, b []byte) {
		out := StripInvisible(b)
		if !json.Valid(b) {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("a JSON body stopped being JSON: %q", out)
		}
		if strings.ContainsFunc(string(out), IsInvisible) {
			t.Fatalf("an invisible character survived: %q", out)
		}
	})
}
