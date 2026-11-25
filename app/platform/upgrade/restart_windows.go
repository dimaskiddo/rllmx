//go:build windows

package upgrade

import "os"

// Restart starts the binary at exe with the same arguments and ends this
// process. It does not return.
func Restart(exe string) error {
	if _, err := os.StartProcess(exe, os.Args, &os.ProcAttr{Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}}); err != nil {
		return err
	}
	os.Exit(0)
	return nil
}
