package auth

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/store/repository"
)

func access(t *testing.T, raw string) (repository.KeyAccess, error) {
	t.Helper()
	return ValidateAccess(gjson.Parse(raw))
}

func TestAnAccessListIsTakenAsItIsMeant(t *testing.T) {
	got, err := access(t, `{"restricted":true,"allow":["openai/gpt-4o","claude/claude-opus-5"," openai/gpt-4o ","","OPENAI/GPT-4O"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Restricted {
		t.Fatal("a restricted key came back unrestricted")
	}
	// Trimmed, de-duplicated case-insensitively with the first spelling kept, and
	// the order the client gave.
	if len(got.Allow) != 2 || got.Allow[0] != "openai/gpt-4o" || got.Allow[1] != "claude/claude-opus-5" {
		t.Fatalf("allow = %v", got.Allow)
	}
}

func TestAnUnrestrictedKeyCarriesNoList(t *testing.T) {
	got, err := access(t, `{"restricted":false,"allow":["openai/gpt-4o"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got.Restricted || len(got.Allow) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAnAccessObjectIsRefusedRatherThanRepaired(t *testing.T) {
	entries := func(n int) string {
		list := make([]string, n)
		for i := range list {
			list[i] = fmt.Sprintf(`"provider/model-%d"`, i)
		}
		return "[" + strings.Join(list, ",") + "]"
	}
	cases := map[string]string{
		"not an object":       `["openai/gpt-4o"]`,
		"no restricted flag":  `{"allow":[]}`,
		"a string flag":       `{"restricted":"yes"}`,
		"an unknown field":    `{"restricted":true,"allow":[],"models":[]}`,
		"allow not a list":    `{"restricted":true,"allow":"openai/gpt-4o"}`,
		"a non-string entry":  `{"restricted":true,"allow":[1]}`,
		"too many entries":    `{"restricted":true,"allow":` + entries(201) + `}`,
		"an overlong entry":   `{"restricted":true,"allow":["` + strings.Repeat("m", 257) + `"]}`,
		"a control character": `{"restricted":true,"allow":["openai/gpt-4o\u0007"]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := access(t, raw); err == nil {
				t.Fatal("the object was accepted")
			}
		})
	}
	if _, err := access(t, `{"restricted":true,"allow":`+entries(200)+`}`); err != nil {
		t.Fatalf("200 entries: %v", err)
	}
	if _, err := access(t, `{"restricted":true,"allow":["`+strings.Repeat("m", 256)+`"]}`); err != nil {
		t.Fatalf("a 256-character entry: %v", err)
	}
}

func TestAKeyIsMaskedWithoutGivingItAway(t *testing.T) {
	key := "sk-abcdef-123456-9f8e7d6c"
	masked := MaskKey(key)
	if masked == key || masked == "" {
		t.Fatalf("mask %q", masked)
	}
	if strings.Contains(masked, "abcdef") || strings.Contains(masked, "9f8e7d6c") {
		t.Fatalf("mask %q carries the key", masked)
	}
	if got := MaskKey(""); got != "(unreadable)" {
		t.Fatalf("an empty key masked to %q", got)
	}
}

func TestMaskKeyIsWhatTheCLIPrintedBeforeTheMove(t *testing.T) {
	// The exact strings keys:list printed, so a key reads the same from either door.
	for key, want := range map[string]string{
		"sk-mach-abc123-9f8e7d6c": "sk-ma…7d6c",
		"short":                   "…ort",
	} {
		if got := MaskKey(key); got != want {
			t.Errorf("MaskKey(%q) = %q, want %q", key, got, want)
		}
	}
}
