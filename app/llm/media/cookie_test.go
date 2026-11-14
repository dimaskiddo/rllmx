package media

import "testing"

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
