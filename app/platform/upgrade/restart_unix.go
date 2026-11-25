//go:build !windows

package upgrade

import (
	"os"
	"syscall"
)

// Restart replaces this process with the binary at exe, keeping its arguments
// and environment. It does not return.
func Restart(exe string) error {
	return syscall.Exec(exe, os.Args, os.Environ())
}
