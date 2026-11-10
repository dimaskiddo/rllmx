package openai

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAToolNameCutNeverSplitsACharacter(t *testing.T) {
	name := strings.Repeat("a", maxToolName-1) + "é" + "tail"
	got := cut(name)
	if len(got) > maxToolName || !utf8.ValidString(got) {
		t.Fatalf("%d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if cut("short") != "short" || cut(strings.Repeat("b", maxToolName)) != strings.Repeat("b", maxToolName) {
		t.Fatal("a name within the limit changed")
	}
}
