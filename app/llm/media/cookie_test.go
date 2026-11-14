package media

import (
	"testing"

	apptext "rllmx/app/platform/text"
)

func TestAPasteIsReadBackInOrder(t *testing.T) {
	j, err := ParseCookie("__client=abc; sso=def; __Secure-next-auth.session-token=ghi")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got, want := j.Header(), "__client=abc; sso=def; __Secure-next-auth.session-token=ghi"; got != want {
		t.Fatalf("Header = %q, want %q", got, want)
	}
	if got := j.Get("sso"); got != "def" {
		t.Fatalf("Get(sso) = %q", got)
	}
}

func TestARotatedValueIsReplacedInPlace(t *testing.T) {
	j, _ := ParseCookie("__client=old; sso=keep")
	if err := j.Set("__client", "new"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, want := j.Header(), "__client=new; sso=keep"; got != want {
		t.Fatalf("Header = %q, want %q", got, want)
	}
	if err := j.Set("__session", "added"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, want := j.Header(), "__client=new; sso=keep; __session=added"; got != want {
		t.Fatalf("Header = %q, want %q", got, want)
	}
}

func TestWhatIsNotACookieIsRefused(t *testing.T) {
	for _, raw := range []string{"", "   ", "no-equals", "a=1\r\nInjected: yes", "Cookie:"} {
		if _, err := ParseCookie(raw); err == nil {
			t.Fatalf("ParseCookie(%q) was accepted", raw)
		}
	}
}

func TestAnInjectedValueIsRefused(t *testing.T) {
	j, _ := ParseCookie("a=1")
	if err := j.Set("b", "x\ry"); err == nil {
		t.Fatal("a control character was accepted")
	}
	if err := j.Set("b c", "v"); err == nil {
		t.Fatal("a name with a space was accepted")
	}
}

// FuzzParseCookie reads any pasted text as a cookie header. It must never
// panic, and whatever it accepts is one it will send: no control character,
// every name set, and a header that parses back to the same jar.
func FuzzParseCookie(f *testing.F) {
	f.Add("a=1; b=2")
	f.Add("__client=abc")
	f.Add("a=1\r\nInjected: x")
	f.Add(";;;=;")
	f.Fuzz(func(t *testing.T, raw string) {
		jar, err := ParseCookie(raw)
		if err != nil {
			return
		}
		header := jar.Header()
		if apptext.HasASCIIControl(header) || header == "" {
			t.Fatalf("accepted %q into an unsendable header %q", raw, header)
		}
		again, err := ParseCookie(header)
		if err != nil || again.Header() != header {
			t.Fatalf("not a fixed point: %q -> %q -> %v", raw, header, err)
		}
	})
}
