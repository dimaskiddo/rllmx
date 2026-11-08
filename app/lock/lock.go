// Package lock keeps one rllmx process at a time on a data directory. The
// lock is the operating system's, so it goes away when the process dies.
package lock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
)

// FileName is the lock file inside the data directory.
const FileName = "rllmx.lock"

// ErrHeld means another process holds the data directory.
var ErrHeld = errors.New("lock: another rllmx process is using this data directory")

// Lock is a held data directory lock.
type Lock struct {
	mu sync.Mutex
	fl *flock.Flock
}

// Acquire takes the lock without waiting. The error wraps ErrHeld and names
// the directory when someone else has it.
func Acquire(dir string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("lock: create %s: %w", dir, err)
	}
	fl := flock.New(filepath.Join(dir, FileName))
	ok, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("lock: %s: %w", dir, err)
	}
	if !ok {
		return nil, fmt.Errorf("%w (%s)", ErrHeld, dir)
	}
	return &Lock{fl: fl}, nil
}

// Release frees the lock. It is safe to call twice and on a nil Lock.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fl == nil {
		return nil
	}
	err := l.fl.Unlock()
	l.fl = nil
	if err != nil {
		return fmt.Errorf("lock: release: %w", err)
	}
	return nil
}
