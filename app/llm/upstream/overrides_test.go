package upstream

import (
	"strings"
	"testing"
)

func TestHeaderOverrides(t *testing.T) {
	raw := `{"openai":{"headers":{"X-A":"1","X-N":2}},"we.ird*id":{"headers":{"X-B":"x"}},"bad":5}`
	cases := []struct {
		provider string
		want     map[string]string
	}{
		{"openai", map[string]string{"X-A": "1"}},
		{"we.ird*id", map[string]string{"X-B": "x"}},
		{"bad", map[string]string{}},
		{"none", map[string]string{}},
	}
	for _, c := range cases {
		got := HeaderOverrides(raw, c.provider)
		if len(got) != len(c.want) {
			t.Fatalf("%s: got %v, want %v", c.provider, got, c.want)
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Fatalf("%s: got %v, want %v", c.provider, got, c.want)
			}
		}
	}
	if len(HeaderOverrides("", "openai")) != 0 || len(HeaderOverrides("not json", "openai")) != 0 {
		t.Fatal("unreadable input must give no headers")
	}
}

// TestBuiltinHeadersAreWhatTheRequestSends covers the defaults the overrides
// page offers: a login's identity headers, the version an auth-less Claude
// provider sends, and none of the keys that hold a template.
func TestBuiltinHeadersAreWhatTheRequestSends(t *testing.T) {
	r, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	claude, ok := r.ByID("claude")
	if !ok {
		t.Fatal("no claude entry")
	}
	got := claude.BuiltinHeaders()
	if got["anthropic-version"] != ClaudeVersion || got["User-Agent"] == "" || got["X-App"] == "" {
		t.Fatalf("claude defaults %v", got)
	}
	if _, ok := got["anthropic-beta"]; ok {
		t.Fatal("the beta list is computed per request, not shown as a default")
	}
	for name := range got {
		if ReservedHeader(name) {
			t.Fatalf("%s is a credential or framing header", name)
		}
	}

	anthropic, ok := r.ByID("anthropic")
	if !ok || anthropic.BuiltinHeaders()["anthropic-version"] != ClaudeVersion {
		t.Fatalf("a key provider carries its own auth headers: %v", anthropic.BuiltinHeaders())
	}

	gemini, ok := r.ByID("gemini-cli")
	if !ok {
		t.Fatal("no gemini-cli entry")
	}
	for name := range gemini.BuiltinHeaders() {
		if strings.HasPrefix(name, "X-Rllmx-") {
			t.Fatalf("a template key was offered as a header: %s", name)
		}
	}
}
