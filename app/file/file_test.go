package file

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteCreatesDirAndFileWithPrivateModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := WritePrivate(dir, "f", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "f"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("read %q err %v", b, err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "f"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("file mode %v", fi.Mode().Perm())
		}
		di, _ := os.Stat(dir)
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", di.Mode().Perm())
		}
	}
}

func TestWriteReplacesAndLeavesNoTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(dir, "f", []byte("new")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "f"))
	if string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(dir, "f"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d entries left", len(entries))
	}
}

func TestWriteFailsWhenTheDirectoryIsAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(p, "f", []byte("x")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWriteRemovesTheTemporaryFileWhenTheRenameFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "f", "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := WritePrivate(dir, "f", []byte("x")); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("%d entries left, want only the directory", len(entries))
	}
}

func TestTouchPrivateCreatesWith0600AndKeepsExistingContent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	if err := TouchPrivate(p); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(p)
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode().Perm())
		}
	}
	if err := os.WriteFile(p, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := TouchPrivate(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "keep" {
		t.Fatalf("existing file changed: %q", b)
	}
}

func TestTouchPrivateFailsWithoutTheFolder(t *testing.T) {
	if err := TouchPrivate(filepath.Join(t.TempDir(), "missing", "x")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestURIEscapesTheFolderAndKeepsTheSchemeOpaque(t *testing.T) {
	got := URI("/data/with space/db#1/x?.sqlite")
	want := "file:/data/with%20space/db%231/x%3F.sqlite"
	if got != want {
		t.Fatalf("URI = %q, want %q", got, want)
	}
}
