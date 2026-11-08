package executor

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEveryInstructionFileHasTextAndNoNameIsMissing(t *testing.T) {
	files, err := fs.Glob(instructionFiles, "instructions/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("no instruction files embedded: %v", err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(strings.TrimPrefix(f, "instructions/"), ".txt")
		if instruction(name) == "" {
			t.Errorf("%s is empty", f)
		}
	}
	for _, name := range []string{"codex"} {
		if instruction(name) == "" {
			t.Errorf("an executor reads %q and there is no text for it", name)
		}
	}
	if instruction("no-such-backend") != "" {
		t.Error("a name with no file returned text")
	}
}

func TestInstructionTextIsTrimmedAndOurOwn(t *testing.T) {
	got := instruction("codex")
	if got != strings.TrimSpace(got) || strings.Contains(got, "Codex CLI") {
		t.Fatalf("instruction text is not trimmed or names the reference: %q", got)
	}
}
