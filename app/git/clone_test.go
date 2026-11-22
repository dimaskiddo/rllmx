package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/platform/network"
)

const source = "https://github.com/acme/skills"

func public(context.Context, string, *network.Allow) error { return nil }

// fakeGit puts a script named git on PATH that records its arguments.
func fakeGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	args := filepath.Join(dir, "args.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(args) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return args
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestClonePassesTheWholeArgumentList(t *testing.T) {
	args := fakeGit(t)
	parent := t.TempDir()
	cfg := Config{Parent: parent, Sources: []string{source}, Check: public}
	dir, err := NewGate().Clone(context.Background(), cfg, source, "main")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != parent {
		t.Fatalf("clone landed in %q", dir)
	}
	raw, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := cloneArgs(source, "main", dir)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("args %q, want %q", got, want)
	}
}

func TestCloneRefusesBeforeAProcessExists(t *testing.T) {
	for name, c := range map[string]struct {
		url, ref string
		sources  []string
	}{
		"http":          {"http://github.com/acme/skills", "main", []string{source}},
		"user info":     {"https://u:p@github.com/acme/skills", "main", []string{source}},
		"not listed":    {source, "main", []string{"https://github.com/other/repo"}},
		"no sources":    {source, "main", nil},
		"path steps up": {"https://github.com/acme/../skills", "main", []string{source}},
		"query":         {source + "?x=1", "main", []string{source}},
		"parent ref":    {source, "v1..2", []string{source}},
		"stepping ref":  {source, "../x", []string{source}},
		"long ref":      {source, strings.Repeat("a", 129), []string{source}},
		"empty ref":     {source, "", []string{source}},
	} {
		args := fakeGit(t)
		err := func() error {
			_, err := NewGate().Clone(context.Background(), Config{
				Parent: t.TempDir(), Sources: c.sources, Check: public,
			}, c.url, c.ref)
			return err
		}()
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
		if _, statErr := os.Stat(args); statErr == nil {
			t.Errorf("%s: git ran", name)
		}
	}
}

func TestAListedSourceMayBeSpelledWithASlashOrSuffix(t *testing.T) {
	for _, listed := range []string{source + "/", source + ".git", source + "/.git"} {
		cfg := Config{Parent: t.TempDir(), Sources: []string{listed}, Check: public}
		if err := cfg.validate(source, "main"); err != nil {
			t.Errorf("%q: %v", listed, err)
		}
	}
}

func TestCloneReportsAMissingGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	cfg := Config{Parent: t.TempDir(), Sources: []string{source}, Check: public}
	_, err := NewGate().Clone(context.Background(), cfg, source, "main")
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err %v", err)
	}
}

func TestCloneDropsTheFolderWhenGitFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	parent := t.TempDir()
	_, err := NewGate().Clone(context.Background(), Config{Parent: parent, Sources: []string{source}, Check: public}, source, "main")
	if !errors.Is(err, ErrFailed) {
		t.Fatalf("err %v", err)
	}
	ents, err := os.ReadDir(parent)
	if err != nil || len(ents) != 0 {
		t.Fatalf("a failed clone left %v (%v)", ents, err)
	}
}

func TestCloneEnvIsOnlyWhatTheFilterAllows(t *testing.T) {
	t.Setenv("SECRET_TOKEN", "leak")
	env := cloneEnv("/tmp/area")
	want := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=/tmp/area",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_ALLOW_PROTOCOL=https",
	}
	if strings.Join(env, "\n") != strings.Join(want, "\n") {
		t.Fatalf("env %q", env)
	}
}

func TestAProcessThatWouldStepOutIsRefused(t *testing.T) {
	cfg := Config{Parent: "", Sources: []string{source}, Check: public}
	if err := cfg.validate(source, "main"); !errors.Is(err, ErrRefused) {
		t.Fatalf("no clone area was accepted: %v", err)
	}
}

func TestCloneFollowsNoRedirect(t *testing.T) {
	args := cloneArgs(source, "main", t.TempDir())
	var on bool
	for i, a := range args {
		if a == "-c" && i+1 < len(args) && args[i+1] == "http.followRedirects=false" {
			on = true
		}
		if a == "clone" && !on {
			t.Fatalf("the redirect switch must precede the sub-command: %q", args)
		}
	}
	if !on {
		t.Fatalf("a redirect could carry the clone past the checked address: %q", args)
	}
}
