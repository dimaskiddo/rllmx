package settings

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// ResolveDataDir turns the DATA_DIR value into an absolute path. A leading ~
// means the home directory; ~name is refused. The error never repeats the value.
func ResolveDataDir(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsRune(p, 0) {
		return "", errors.New("settings: DATA_DIR: must be a non-empty path")
	}
	if rest, ok := strings.CutPrefix(p, "~"); ok {
		if rest != "" && rest[0] != '/' && rest[0] != filepath.Separator {
			return "", errors.New("settings: DATA_DIR: ~user paths are not supported")
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("settings: DATA_DIR: cannot resolve the home directory")
		}
		p = filepath.Join(home, rest)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", errors.New("settings: DATA_DIR: cannot resolve the path")
	}
	return abs, nil
}
