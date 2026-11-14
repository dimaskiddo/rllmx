package upstream

import (
	"strings"
	"testing"

	"rllmx/app/platform/text"
)

func TestParseWebCredentialForms(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"bare token", "userToken-abc", "userToken-abc", true},
		{"bearer prefix", "Bearer  tok123", "tok123", true},
		{"single pair", "sso=abc123", "sso=abc123", true},
		{"header with spaces", "  sso = abc ; cf_clearance = xyz  ", "sso=abc; cf_clearance=xyz", true},
		{"cookie prefix", "Cookie: sso=abc", "sso=abc", true},
		{"cookie prefix lowercase", "cookie: sso=abc", "sso=abc", true},
		{"base64 padding kept", "token=aGVsbG8=", "token=aGVsbG8=", true},
		{"trailing semicolon", "a=b;", "a=b", true},
		{"cookie editor json", `[{"name":"sso","value":"abc","domain":".grok.com"}]`, "sso=abc", true},
		{"cookie editor object", `{"cookies":[{"name":"sso","value":"abc","domain":".grok.com"}]}`, "sso=abc", true},
		{"cookie object with an empty array", `{"cookies":[]}`, "", false},
		{"cookie object entry with a bad name", `{"cookies":[{"name":"a b","value":"c"}]}`, "", false},
		{"local storage json", `{"value":"deepseek-token","__version":"0"}`, "deepseek-token", true},
		{"empty", "", "", false},
		{"blank", "   ", "", false},
		{"control character", "a=b\x00c", "", false},
		{"newline", "a=b\nc=d", "", false},
		{"space in a bare value", "two words", "", false},
		{"bad name", "bad name=x", "", false},
		{"empty value", "a=", "", false},
		{"semicolon in value", "a=b;c", "", false},
		{"no value after equals", "a=", "", false},
		{"broken json", `{"value":`, "", false},
		{"json without a value", `{"other":1}`, "", false},
		{"empty json array", `[]`, "", false},
		{"json entry with a bad name", `[{"name":"a b","value":"c"}]`, "", false},
		{"too long", strings.Repeat("a", MaxWebCredential+1), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseWebCredential(c.in)
			if ok != c.ok || got != c.want {
				t.Fatalf("ParseWebCredential(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestCookieNameAndValue(t *testing.T) {
	for _, n := range []string{"sso", "__Secure-next-auth.session-token", "Hm_lvt_abc", "a-b_c.1"} {
		if !CookieName(n) {
			t.Errorf("CookieName(%q) = false, want true", n)
		}
	}
	for _, n := range []string{"", "a b", "a;b", "a=b", strings.Repeat("x", 129)} {
		if CookieName(n) {
			t.Errorf("CookieName(%q) = true, want false", n)
		}
	}
	for _, v := range []string{"abc", "a=b", "eyJhbGciOi.eyJzdWIiOi.abc"} {
		if !CookieValue(v) {
			t.Errorf("CookieValue(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"", "a b", `a"b`, "a,b", "a;b"} {
		if CookieValue(v) {
			t.Errorf("CookieValue(%q) = true, want false", v)
		}
	}
}

func FuzzParseWebCredential(f *testing.F) {
	f.Add("")
	f.Add("sso=abc; cf_clearance=xyz")
	f.Add(`{"value":"tok"}`)
	f.Add(`[{"name":"a","value":"b"}]`)
	f.Add(`{"cookies":[{"name":"a","value":"b"}]}`)
	f.Add("Bearer tok")
	f.Fuzz(func(t *testing.T, in string) {
		out, ok := ParseWebCredential(in)
		if !ok {
			return
		}
		// Whatever is accepted must be re-accepted: the normalized form is a
		// fixed point, and no control character ever survives into it.
		again, ok2 := ParseWebCredential(out)
		if !ok2 || again != out {
			t.Fatalf("not idempotent: %q -> (%q, %v) -> (%q, %v)", in, out, ok, again, ok2)
		}
		if text.HasControl(out) {
			t.Fatalf("control character survived: %q", out)
		}
	})
}
