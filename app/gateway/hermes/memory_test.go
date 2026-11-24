package hermes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMem(t *testing.T, home, name, body string) string {
	t.Helper()
	dir := filepath.Join(home, "memories")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheMemoryBlockJoinsBothFiles(t *testing.T) {
	home := t.TempDir()
	writeMem(t, home, "MEMORY.md", "the router is Go\n§\nusage rows hold metadata")
	writeMem(t, home, "USER.md", "prefers short answers")
	r := &Reader{Home: home}
	got := r.Block(0)
	for _, want := range []string{"Agent Persistent Memory", "the router is Go", "usage rows hold metadata", "USER PROFILE", "prefers short answers"} {
		if !strings.Contains(got, want) {
			t.Errorf("block %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "§") {
		t.Error("the separator reached the block")
	}
}

func TestAnEmptyOrMissingStoreInjectsNothing(t *testing.T) {
	if got := (&Reader{Home: t.TempDir()}).Block(0); got != "" {
		t.Fatalf("an empty store answered %q", got)
	}
	if got := (&Reader{}).Block(0); got != "" {
		t.Fatalf("no home answered %q", got)
	}
	var nilReader *Reader
	if got := nilReader.Block(0); got != "" {
		t.Fatalf("a nil reader answered %q", got)
	}
}

func TestTheBlockIsCutAtTheLimit(t *testing.T) {
	home := t.TempDir()
	writeMem(t, home, "MEMORY.md", strings.Repeat("note ", 400))
	got := (&Reader{Home: home}).Block(500)
	if len(got) > 500 || got == "" {
		t.Fatalf("block is %d bytes", len(got))
	}
}

func TestASingleEntryIsCutAndTheBlockIsCached(t *testing.T) {
	home := t.TempDir()
	path := writeMem(t, home, "MEMORY.md", strings.Repeat("x", 900))
	r := &Reader{Home: home}
	got := r.Block(0)
	if !strings.Contains(got, "...") || len(got) > DefaultLimit {
		t.Fatalf("an entry was not cut: %d bytes", len(got))
	}
	// A rewrite moves the mtime, so the next call reads the file again.
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if again := r.Block(0); again != got {
		t.Fatal("the same content rendered differently")
	}
	writeMem(t, home, "MEMORY.md", "a new note")
	later = time.Now().Add(4 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if again := r.Block(0); !strings.Contains(again, "a new note") {
		t.Fatalf("the cache did not notice the change: %q", again)
	}
}
