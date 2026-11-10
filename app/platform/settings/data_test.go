package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDataDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	abs, _ := filepath.Abs("relative/dir")
	cases := []struct {
		in, want string
	}{
		{"~", home},
		{"~/rllmx-data", filepath.Join(home, "rllmx-data")},
		{"relative/dir", abs},
		{"/var/lib/rllmx", "/var/lib/rllmx"},
		{"/var/lib/rllmx/../rllmx2", "/var/lib/rllmx2"},
	}
	for _, c := range cases {
		got, err := ResolveDataDir(c.in)
		if err != nil || got != c.want {
			t.Errorf("%q -> %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestResolveDataDirRefusesAUserHome(t *testing.T) {
	for _, in := range []string{"~other/x", "~root", "~\x00"} {
		if _, err := ResolveDataDir(in); err == nil || !strings.Contains(err.Error(), "DATA_DIR") {
			t.Errorf("%q: error = %v", in, err)
		}
	}
}

func TestResolveDataDirRefusesAnEmptyOrNulPath(t *testing.T) {
	for _, in := range []string{"", "   ", "a\x00b"} {
		if _, err := ResolveDataDir(in); err == nil || !strings.Contains(err.Error(), "DATA_DIR") {
			t.Errorf("%q: error = %v", in, err)
		}
	}
}

func TestResolveDataDirErrorsNeverQuoteThePath(t *testing.T) {
	_, err := ResolveDataDir("~secret-user-name/x")
	if err == nil || strings.Contains(err.Error(), "secret-user-name") {
		t.Fatalf("error = %v", err)
	}
}
