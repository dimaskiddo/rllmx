// Package git is the only code that starts a process. It runs one allowlisted
// clone in a minimal environment and refuses every other program, subcommand
// and argument before a process exists.
package git

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"rllmx/app/platform/network"
)

// Bounds on one clone: it runs only on an admin action, so it is allowed to
// take a while, but never without a ceiling.
const (
	// Timeout bounds one clone or download.
	Timeout = 60 * time.Second
	// MaxSize is the on-disk cap, checked while the clone runs.
	MaxSize = 64 << 20
)

var (
	// ErrRefused means a filter refused the request before a process existed.
	ErrRefused = errors.New("git: the request is not allowed")
	// ErrFailed means the process ran and did not finish the job.
	ErrFailed = errors.New("git: the clone failed")

	reRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
)

// Config bounds one clone: where it lands, the exact https sources the operator
// permits, and the outbound guard's list.
type Config struct {
	Parent  string
	Sources []string
	Allow   *network.Allow
	// Check refuses a source host that is not plainly public. Nil uses the
	// network guard; a test sets its own.
	Check func(ctx context.Context, raw string, allow *network.Allow) error
}

// Gate is the process launcher. One instance per program, so two admin actions
// never clone at once.
type Gate struct {
	mu sync.Mutex
}

// NewGate returns the launcher.
func NewGate() *Gate { return &Gate{} }

// Clone fetches url at ref into a new folder under cfg.Parent and returns that
// folder. Every check runs before the process exists; a failure removes the
// half-written folder.
func (g *Gate) Clone(ctx context.Context, cfg Config, rawURL, ref string) (string, error) {
	if err := cfg.validate(rawURL, ref); err != nil {
		return "", err
	}
	if err := cfg.check(ctx, rawURL); err != nil {
		return "", fmt.Errorf("%w: %w", ErrRefused, err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("%w: git is not installed", ErrFailed)
	}
	if err := os.MkdirAll(cfg.Parent, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(cfg.Parent, ".clone-")
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitPath, cloneArgs(rawURL, ref, dir)...)
	cmd.Dir = cfg.Parent
	cmd.Env = cloneEnv(cfg.Parent)
	stop := watchSize(ctx, cancel, dir)
	err = cmd.Run()
	close(stop)
	if err != nil {
		_ = os.RemoveAll(dir)
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: it timed out or was cancelled", ErrFailed)
		}
		return "", fmt.Errorf("%w: %s", ErrFailed, err)
	}
	return dir, nil
}

// validate is every filter that precedes a process, in one place.
func (c Config) validate(rawURL, ref string) error {
	if c.Parent == "" {
		return fmt.Errorf("%w: no clone area", ErrRefused)
	}
	if !reRef.MatchString(ref) || strings.Contains(ref, "..") {
		return fmt.Errorf("%w: the ref is not a plain branch or tag name", ErrRefused)
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: the source must be a plain https URL", ErrRefused)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%w: the source must carry no query or fragment", ErrRefused)
	}
	if slices.Contains(strings.Split(u.Path, "/"), "..") {
		return fmt.Errorf("%w: the source path may not step up", ErrRefused)
	}
	if slices.ContainsFunc(c.Sources, func(listed string) bool { return sameSource(listed, rawURL) }) {
		return nil
	}
	return fmt.Errorf("%w: the source is not on the operator's list", ErrRefused)
}

func (c Config) check(ctx context.Context, raw string) error {
	if c.Check != nil {
		return c.Check(ctx, raw, c.Allow)
	}
	return network.CheckPublic(ctx, raw, c.Allow)
}

// sameSource compares two addresses after dropping a trailing slash and a .git
// suffix, so the list may spell the repository either way.
func sameSource(listed, raw string) bool {
	return normalizeSource(listed) == normalizeSource(raw)
}

func normalizeSource(s string) string {
	s = strings.TrimSuffix(strings.TrimSpace(s), "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.TrimSuffix(s, "/")
}

// cloneArgs is the whole argument list: a fixed set of flags, then the address
// and the folder after a -- that ends option parsing. Nothing is ever joined
// into a shell string.
func cloneArgs(rawURL, ref, dir string) []string {
	return []string{
		"clone", "--depth", "1", "--single-branch", "--no-tags",
		"--branch", ref, "--", rawURL, dir,
	}
}

// cloneEnv is the whole environment: no inherited secret, credential helper or
// git configuration.
func cloneEnv(home string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ALLOW_PROTOCOL=https",
	}
}

// watchSize cancels a clone that outgrows MaxSize while it runs.
func watchSize(ctx context.Context, cancel context.CancelFunc, dir string) chan struct{} {
	stop := make(chan struct{})
	go func() {
		timer := time.NewTicker(250 * time.Millisecond)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-timer.C:
				if dirSize(dir) > MaxSize {
					cancel()
					return
				}
			}
		}
	}()
	return stop
}

func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if total > MaxSize {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
