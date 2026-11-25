// Package upgrade downloads this program's own release for the running
// platform, checks it against the release's checksum file, replaces the binary
// and can re-execute it. It is the one place the program acts on its own file;
// nothing else may.
package upgrade

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// ReleasesURL is the newest release of this repository; a constant, so no
	// request can name another address.
	ReleasesURL = "https://api.github.com/repos/dimaskiddo/rllmx/releases/latest"
	// maxArchive bounds the download; the guarded client bounds it again.
	maxArchive = 64 << 20
	// maxChecksums bounds the checksum file.
	maxChecksums = 1 << 20
)

// ErrDevBuild reports a binary with no release version to compare.
var ErrDevBuild = errors.New("upgrade: this build has no version")

// ErrPlatform reports a release with no asset for this platform.
var ErrPlatform = errors.New("upgrade: the release names no asset for this platform")

// ErrChecksum reports a download that does not match its published digest.
var ErrChecksum = errors.New("upgrade: the download does not match its checksum")

// Fetcher reads one address through the guarded client.
type Fetcher func(ctx context.Context, address string) ([]byte, error)

// Options name the build and where its binary lives.
type Options struct {
	Version string
	// GOOS and GOARCH default to the running platform.
	GOOS, GOARCH string
	// Exe is the binary to replace; empty takes os.Executable.
	Exe   string
	Fetch Fetcher
	// Now is the clock; nil uses time.Now.
	Now func() time.Time
}

// release is the part of GitHub's answer the installer reads.
type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// Result is one completed install.
type Result struct {
	Exe    string
	Latest string
}

// Version trims a tag's "v" so one spelling compares equal.
func Version(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// AssetName is the archive this platform releases, as the build writes it.
func AssetName(version, goos, goarch string) string {
	os := goos
	if os == "darwin" {
		os = "macos"
	}
	arch := goarch
	switch goarch {
	case "amd64":
		arch = "64-bit"
	case "arm64":
		arch = "arm-64-bit"
	}
	return "rllmx_" + Version(version) + "_" + os + "_" + arch + ".zip"
}

// Newest reads the newest release and the asset this platform runs.
func Newest(ctx context.Context, o Options) (latest, assetURL, checksumsURL string, err error) {
	if Version(o.Version) == "" || o.Version == "dev" {
		return "", "", "", ErrDevBuild
	}
	if o.Fetch == nil {
		return "", "", "", errors.New("upgrade: no fetcher is configured")
	}
	body, err := o.Fetch(ctx, ReleasesURL)
	if err != nil {
		return "", "", "", err
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", "", "", fmt.Errorf("upgrade: the release answer is unreadable: %w", err)
	}
	latest = Version(rel.TagName)
	if latest == "" {
		return "", "", "", errors.New("upgrade: the release names no version")
	}
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	want := AssetName(latest, goos, goarch)
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			assetURL = a.URL
		case "checksums.txt":
			checksumsURL = a.URL
		}
	}
	if assetURL == "" {
		return "", "", "", ErrPlatform
	}
	return latest, assetURL, checksumsURL, nil
}

// Check reports the newest release without touching the binary.
func Check(ctx context.Context, o Options) (string, error) {
	latest, _, _, err := Newest(ctx, o)
	return latest, err
}

// Install downloads the release, verifies its digest, unpacks the binary and
// swaps it in. A failure before the swap leaves the current binary in place; the
// old one is kept beside it as .old.
func Install(ctx context.Context, o Options) (Result, error) {
	latest, assetURL, sumsURL, err := Newest(ctx, o)
	if err != nil {
		return Result{}, err
	}
	if sumsURL == "" {
		return Result{}, errors.New("upgrade: the release publishes no checksums")
	}
	exe := o.Exe
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return Result{}, fmt.Errorf("upgrade: cannot find this binary: %w", err)
		}
	}
	sums, err := o.Fetch(ctx, sumsURL)
	if err != nil {
		return Result{}, err
	}
	if len(sums) > maxChecksums {
		return Result{}, errors.New("upgrade: the checksum file is too large")
	}
	archive, err := o.Fetch(ctx, assetURL)
	if err != nil {
		return Result{}, err
	}
	if len(archive) > maxArchive {
		return Result{}, errors.New("upgrade: the release is too large")
	}
	if err := verify(sums, AssetName(latest, goosOf(o), archOf(o)), archive); err != nil {
		return Result{}, err
	}
	bin, err := unpack(archive, goosOf(o))
	if err != nil {
		return Result{}, err
	}
	if err := swap(exe, bin); err != nil {
		return Result{}, err
	}
	return Result{Exe: exe, Latest: latest}, nil
}

func goosOf(o Options) string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func archOf(o Options) string {
	if o.GOARCH != "" {
		return o.GOARCH
	}
	return runtime.GOARCH
}

// verify compares the download with the line the release publishes for it.
func verify(sums []byte, name string, archive []byte) error {
	sum := sha256.Sum256(archive)
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = strings.ToLower(fields[0])
			break
		}
	}
	if want == "" {
		return fmt.Errorf("%w: the release lists no digest for %s", ErrChecksum, name)
	}
	if hex.EncodeToString(sum[:]) != want {
		return ErrChecksum
	}
	return nil
}

// unpack returns the binary inside the archive. Any other entry is refused.
func unpack(archive []byte, goos string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("upgrade: the release is not a zip: %w", err)
	}
	name := "rllmx"
	if goos == "windows" {
		name = "rllmx.exe"
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != name || f.FileInfo().IsDir() {
			continue
		}
		return readEntry(f)
	}
	return nil, fmt.Errorf("upgrade: the release holds no %s", name)
}

// readEntry reads one archive entry whole. One past the limit is read so a
// larger binary is refused instead of installed cut short.
func readEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, maxArchive+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxArchive {
		return nil, errors.New("upgrade: the binary in the release is too large")
	}
	if len(body) == 0 {
		return nil, errors.New("upgrade: the release holds an empty binary")
	}
	return body, nil
}

// swap writes the new binary beside the old one and renames both, so a failure
// leaves the running binary where it was.
func swap(exe string, body []byte) error {
	next := exe + ".new"
	old := exe + ".old"
	// O_EXCL makes the file the lock: a second install fails here, before it
	// moves the binary, and a crashed one leaves a file the error names.
	f, err := os.OpenFile(next, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("upgrade: cannot write %s (another update may be running; remove it if not): %w", next, err)
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(next)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(next)
		return err
	}
	if err := os.Chmod(next, 0o755); err != nil {
		os.Remove(next)
		return err
	}
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		os.Remove(next)
		return fmt.Errorf("upgrade: cannot set the old binary aside: %w", err)
	}
	if err := os.Rename(next, exe); err != nil {
		_ = os.Rename(old, exe)
		return fmt.Errorf("upgrade: cannot place the new binary: %w", err)
	}
	return nil
}
