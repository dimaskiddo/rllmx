package cursor

import (
	"testing"
	"time"
)

func TestChecksumMatchesAnIndependentVector(t *testing.T) {
	// Computed outside Go from the format's description: six bytes of seconds, a rolling XOR from 165, then unpadded URL-safe base64.
	got := checksum("abc123", time.Unix(1700000000, 0))
	if got != "pabFmWxxabc123" {
		t.Fatalf("checksum = %q, want pabFmWxxabc123", got)
	}
}

func TestSessionIdIsAStableUUIDv5(t *testing.T) {
	// uuid.NewSHA1 over the DNS namespace, the same derivation the service's own
	// client uses.
	if got := sessionID("tok"); got != "59eb2843-612f-503a-8ceb-68918ca1801e" {
		t.Fatalf("session = %q", got)
	}
	if first, second := sessionID("tok"), sessionID("tok"); first != second {
		t.Fatalf("the same token must keep one session: %q then %q", first, second)
	}
}

func TestBareTokenDropsTheVendorPrefix(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"workos::abc", "abc"},
		{"token", "token"},
		{"a::b::c", "c"},
	} {
		if got := bareToken(tc.in); got != tc.want {
			t.Errorf("bareToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestHeadersCarryNoHostFacts(t *testing.T) {
	h := headers("tok", "machine", true)
	for _, k := range []string{"x-cursor-client-os", "x-cursor-client-arch", "x-cursor-timezone", "x-ghost-mode"} {
		if h[k] == "" {
			t.Fatalf("%s is missing", k)
		}
	}
	if h["x-cursor-timezone"] != "UTC" {
		t.Errorf("timezone = %q, want UTC", h["x-cursor-timezone"])
	}
	if h["x-ghost-mode"] != "true" {
		t.Errorf("ghost = %q, want true", h["x-ghost-mode"])
	}
	if h["Authorization"] != "Bearer tok" {
		t.Errorf("authorization = %q", h["Authorization"])
	}
}

func TestValidMachineHoldsItToAPlainToken(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"abc123", true},
		{"", false},
		{"a b", false},
		{"a\tb", false},
		{string(make([]byte, 200)), false},
	} {
		if got := validMachine(tc.in); got != tc.want {
			t.Errorf("validMachine(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestComposerModelMatchesTheFamily(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  bool
	}{
		{"composer-1", true},
		{"composer", true},
		{"vendor/composer-2", true},
		{"composers", false},
		{"claude-4.5-sonnet", false},
	} {
		if got := composerModel(tc.model); got != tc.want {
			t.Errorf("composerModel(%q) = %v, want %v", tc.model, got, tc.want)
		}
	}
}
