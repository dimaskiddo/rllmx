// Package file writes small private files that must never be seen half written.
package file

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
)

// WritePrivate puts data in dir/name through a temporary file and a rename.
// The directory is created 0700 and the file is 0600.
func WritePrivate(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("file: create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return fmt.Errorf("file: write %s: %w", name, err)
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o600)
	}
	if err == nil {
		err = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if err == nil {
		err = syncDir(dir)
	}
	if err != nil {
		return fmt.Errorf("file: write %s: %w", name, err)
	}
	return nil
}

// TouchPrivate creates path with mode 0600 when it is missing and leaves an
// existing file alone.
func TouchPrivate(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("file: touch %s: %w", filepath.Base(path), err)
	}
	return f.Close()
}

// syncDir makes the rename durable. Windows cannot open a directory for
// syncing, and there a rename is already as durable as the system makes it.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

// URI is the file: URI of a path, with spaces and the characters that start a
// fragment or a query escaped, because data folders may contain them.
func URI(path string) string {
	u := url.URL{Scheme: "file", Opaque: (&url.URL{Path: path}).EscapedPath()}
	return u.String()
}
