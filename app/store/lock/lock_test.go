package lock

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAcquireTakesTheLockAndReleaseFreesIt(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(dir); !errors.Is(err, ErrHeld) {
		t.Fatalf("second Acquire err = %v, want ErrHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(dir)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	_ = again.Release()
}

func TestTheHeldErrorNamesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	_, err = Acquire(dir)
	if err == nil || !strings.Contains(err.Error(), dir) {
		t.Fatalf("error %v should name %s", err, dir)
	}
}

func TestReleaseIsIdempotentAndNilSafe(t *testing.T) {
	l, err := Acquire(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := l.Release(); err != nil {
			t.Fatalf("Release: %v", err)
		}
	}
	var none *Lock
	if err := none.Release(); err != nil {
		t.Fatalf("nil Release: %v", err)
	}
}

func TestLockFileIsPrivateAndNamed(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if FileName != "rllmx.lock" {
		t.Fatalf("file name %q", FileName)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file mode %v", fi.Mode().Perm())
	}
}

func TestAcquireCreatesTheDirectoryPrivately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(dir)
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", fi.Mode().Perm())
		}
	}
}

func TestAcquireFailsWhenTheDirectoryIsAFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p); err == nil || errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
}
