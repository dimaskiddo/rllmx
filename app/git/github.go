package git

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// maxEntries bounds one tarball, so a catalog cannot make the extractor loop
// without end.
const maxEntries = 20000

var reRepoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

// Fetch clones url at ref, or downloads the GitHub tarball when git is missing
// or the clone fails. The caller runs the returned cleanup, which removes the
// folder.
func (g *Gate) Fetch(ctx context.Context, cfg Config, client *http.Client, rawURL, ref string) (string, func(), error) {
	dir, err := g.Clone(ctx, cfg, rawURL, ref)
	if err == nil {
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	dir, derr := g.download(ctx, cfg, client, rawURL, ref)
	if derr != nil {
		return "", nil, fmt.Errorf("%w; the tarball fallback said: %w", err, derr)
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// download fetches a GitHub repository tarball, for a host where git is missing
// or the clone failed. It applies the same source, ref and network checks,
// derives the API address from the source, and extracts only regular files.
func (g *Gate) download(ctx context.Context, cfg Config, client *http.Client, rawURL, ref string) (string, error) {
	if err := cfg.validate(rawURL, ref); err != nil {
		return "", err
	}
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", fmt.Errorf("%w: only github.com has a tarball fallback", ErrRefused)
	}
	owner, repo, ok := splitRepo(u.Path)
	if !ok {
		return "", fmt.Errorf("%w: the source path is not owner/repository", ErrRefused)
	}
	if client == nil {
		return "", errors.New("git: no client is configured")
	}
	if err := cfg.check(ctx, rawURL); err != nil {
		return "", fmt.Errorf("%w: %w", ErrRefused, err)
	}
	if err := os.MkdirAll(cfg.Parent, 0o700); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	api := "https://api.github.com/repos/" + owner + "/" + repo + "/tarball/" + url.PathEscape(ref)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "rllmx")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrFailed, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: the catalog answered %d", ErrFailed, resp.StatusCode)
	}
	return extractTarGz(resp.Body, cfg.Parent)
}

// splitRepo reads owner/repository from a github.com path.
func splitRepo(raw string) (owner, repo string, ok bool) {
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) != 2 || !reRepoPart.MatchString(parts[0]) || !reRepoPart.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], strings.TrimSuffix(parts[1], ".git"), true
}

// extractTarGz writes the tarball's single root folder's contents into a new
// folder and returns it. Symlinks, hard links and devices are dropped, and an
// entry that steps out of the folder aborts the extraction.
func extractTarGz(r io.Reader, parent string) (string, error) {
	return extract(r, parent, MaxSize)
}

func extract(r io.Reader, parent string, max int64) (string, error) {
	dir, err := os.MkdirTemp(parent, ".download-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	gz, err := gzip.NewReader(io.LimitReader(r, MaxSize+1))
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var top string
	var entries int
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		entries++
		if entries > maxEntries {
			return "", errors.New("git: the tarball holds too many entries")
		}
		// GitHub opens every tarball with a global header that carries the commit
		// id and no file.
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		// A backslash or a drive colon steps out of the folder on Windows, where
		// path.Clean does not see it.
		if strings.ContainsAny(h.Name, `\:`) {
			return "", errors.New("git: the tarball steps out of its folder")
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." || name == "" {
			continue
		}
		if strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") {
			return "", errors.New("git: the tarball steps out of its folder")
		}
		first, rest, _ := strings.Cut(name, "/")
		if top == "" {
			top = first
		}
		if first != top {
			return "", errors.New("git: the tarball has more than one root folder")
		}
		if rest == "" {
			continue
		}
		target := filepath.Join(dir, filepath.FromSlash(rest))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return "", err
			}
		case tar.TypeReg:
			total += h.Size
			if total > max {
				return "", errors.New("git: the tarball is too large")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return "", err
			}
			if err := writeEntry(target, tr, h.Size); err != nil {
				return "", err
			}
		}
	}
	keep = true
	return dir, nil
}

func writeEntry(target string, r io.Reader, size int64) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, r, size)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
