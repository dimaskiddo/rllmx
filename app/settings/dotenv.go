package settings

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	"github.com/subosito/gotenv"

	"rllmx/app/file"
)

const maxDotenvBytes = 1 << 20

// ReadKeys returns the sorted, unique variable names in the file at path. The
// framework's Config exposes values by key but no list of keys, so the file is
// parsed again with gotenv, the parser the framework's own loader uses (it also
// strips a byte order mark). A missing file has no keys. Only names leave this
// function, and the parser's errors, which can quote a value, are replaced by
// one that names the file. The size cap is ours: the framework has none.
func ReadKeys(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), errors.Unwrap(err))
	}
	defer f.Close()

	raw, err := file.ReadCapped(f, maxDotenvBytes)
	if errors.Is(err, file.ErrTooLarge) {
		return nil, fmt.Errorf("%s: file larger than %d bytes", filepath.Base(path), maxDotenvBytes)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), errors.Unwrap(err))
	}

	vars, err := gotenv.StrictParse(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: invalid syntax", filepath.Base(path))
	}
	delete(vars, "")
	return slices.Sorted(maps.Keys(vars)), nil
}

// CheckEnvFile reads the names in the file at path and checks them.
func CheckEnvFile(path string) error {
	keys, err := ReadKeys(path)
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	return CheckNames(keys)
}
