package upgrade

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archiveOf builds a zip holding one entry.
func archiveOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fetcher serves the release answer, the checksum file and the archive.
func fetcher(t *testing.T, assetName string, archive []byte, sums string) Fetcher {
	t.Helper()
	release := `{"tag_name":"v0.9.0","assets":[` +
		`{"name":"` + assetName + `","browser_download_url":"asset://zip"},` +
		`{"name":"checksums.txt","browser_download_url":"asset://sums"}]}`
	return func(_ context.Context, address string) ([]byte, error) {
		switch address {
		case ReleasesURL:
			return []byte(release), nil
		case "asset://zip":
			return archive, nil
		case "asset://sums":
			return []byte(sums), nil
		}
		return nil, errors.New("unexpected address")
	}
}

func TestTheAssetNameFollowsTheReleaseTemplate(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "rllmx_0.9.0_linux_64-bit.zip"},
		{"linux", "arm64", "rllmx_0.9.0_linux_arm-64-bit.zip"},
		{"darwin", "arm64", "rllmx_0.9.0_macos_arm-64-bit.zip"},
		{"windows", "amd64", "rllmx_0.9.0_windows_64-bit.zip"},
	} {
		if got := AssetName("v0.9.0", tc.goos, tc.goarch); got != tc.want {
			t.Errorf("%s/%s: %q", tc.goos, tc.goarch, got)
		}
	}
}

func TestAnInstallReplacesTheBinaryAndKeepsTheOldOne(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rllmx")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := AssetName("v0.9.0", "linux", "amd64")
	archive := archiveOf(t, "rllmx", "new")
	sum := sha256.Sum256(archive)
	res, err := Install(context.Background(), Options{
		Version: "0.8.0", GOOS: "linux", GOARCH: "amd64", Exe: exe,
		Fetch: fetcher(t, name, archive, hex.EncodeToString(sum[:])+"  "+name+"\n"),
	})
	if err != nil || res.Latest != "0.9.0" {
		t.Fatalf("%v %+v", err, res)
	}
	if body, _ := os.ReadFile(exe); string(body) != "new" {
		t.Fatalf("the binary is %q", body)
	}
	if body, _ := os.ReadFile(exe + ".old"); string(body) != "old" {
		t.Fatalf("the old binary is %q", body)
	}
}

func TestABadDownloadLeavesTheBinaryAlone(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rllmx")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := AssetName("v0.9.0", "linux", "amd64")
	archive := archiveOf(t, "rllmx", "new")
	for label, sums := range map[string]string{
		"wrong digest": strings.Repeat("0", 64) + "  " + name + "\n",
		"no line":      "",
	} {
		_, err := Install(context.Background(), Options{
			Version: "0.8.0", GOOS: "linux", GOARCH: "amd64", Exe: exe,
			Fetch: fetcher(t, name, archive, sums),
		})
		if !errors.Is(err, ErrChecksum) {
			t.Errorf("%s: %v", label, err)
		}
		if body, _ := os.ReadFile(exe); string(body) != "old" {
			t.Fatalf("%s changed the binary to %q", label, body)
		}
	}
}

func TestAnArchiveWithAnotherEntryIsRefused(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rllmx")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := AssetName("v0.9.0", "linux", "amd64")
	archive := archiveOf(t, "../../etc/passwd", "evil")
	sum := sha256.Sum256(archive)
	if _, err := Install(context.Background(), Options{
		Version: "0.8.0", GOOS: "linux", GOARCH: "amd64", Exe: exe,
		Fetch: fetcher(t, name, archive, hex.EncodeToString(sum[:])+"  "+name+"\n"),
	}); err == nil {
		t.Fatal("an archive with no binary was installed")
	}
	if body, _ := os.ReadFile(exe); string(body) != "old" {
		t.Fatalf("the binary is %q", body)
	}
}

func TestADevBuildNeverReachesTheNetwork(t *testing.T) {
	called := false
	_, err := Install(context.Background(), Options{
		Version: "dev", Exe: filepath.Join(t.TempDir(), "rllmx"),
		Fetch: func(context.Context, string) ([]byte, error) {
			called = true
			return nil, nil
		},
	})
	if !errors.Is(err, ErrDevBuild) || called {
		t.Fatalf("%v called=%v", err, called)
	}
}

func TestABinaryPastTheLimitIsRefusedNotCut(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rllmx")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := AssetName("v0.9.0", "linux", "amd64")
	archive := archiveOf(t, "rllmx", strings.Repeat("\x00", maxArchive+1))
	sum := sha256.Sum256(archive)
	if _, err := Install(context.Background(), Options{
		Version: "0.8.0", GOOS: "linux", GOARCH: "amd64", Exe: exe,
		Fetch: fetcher(t, name, archive, hex.EncodeToString(sum[:])+"  "+name+"\n"),
	}); err == nil {
		t.Fatal("a binary larger than the limit was installed cut short")
	}
	if body, _ := os.ReadFile(exe); string(body) != "old" {
		t.Fatalf("the binary is %d bytes", len(body))
	}
}

func TestASecondInstallBesideAnUnfinishedOneTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "rllmx")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe+".new", []byte("half"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := swap(exe, []byte("new")); err == nil {
		t.Fatal("a second install ran beside an unfinished one")
	}
	if body, _ := os.ReadFile(exe); string(body) != "old" {
		t.Fatalf("the binary is %q", body)
	}
	if body, _ := os.ReadFile(exe + ".new"); string(body) != "half" {
		t.Fatalf("the other install's file is %q", body)
	}
}
