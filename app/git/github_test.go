package git

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

type entry struct {
	name string
	body string
	dir  bool
	size int64
}

func tarball(t *testing.T, entries ...entry) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o600, Typeflag: tar.TypeReg, Size: int64(len(e.body))}
		if e.dir {
			h.Typeflag = tar.TypeDir
			h.Mode = 0o700
		}
		if e.size > 0 {
			h.Size = e.size
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if !e.dir && e.size == 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

func TestExtractWritesTheRepositoryRoot(t *testing.T) {
	dir, err := extractTarGz(tarball(t,
		entry{name: "acme-skills-abc123/", dir: true},
		entry{name: "acme-skills-abc123/skills/graphify/SKILL.md", body: "text"},
	), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "skills", "graphify", "SKILL.md"))
	if err != nil || string(raw) != "text" {
		t.Fatalf("file %q err %v", raw, err)
	}
}

func TestExtractRefusesToStepOut(t *testing.T) {
	parent := t.TempDir()
	if _, err := extractTarGz(tarball(t, entry{name: "acme/../../evil", body: "x"}), parent); err == nil {
		t.Fatal("a stepping entry was extracted")
	}
	if _, err := os.Stat(filepath.Join(parent, "evil")); err == nil {
		t.Fatal("an entry landed outside the folder")
	}
}

func TestExtractRefusesTwoRoots(t *testing.T) {
	if _, err := extractTarGz(tarball(t,
		entry{name: "one/a", body: "x"},
		entry{name: "two/b", body: "y"},
	), t.TempDir()); err == nil {
		t.Fatal("two roots were accepted")
	}
}

func TestExtractRefusesAnOversizedEntry(t *testing.T) {
	if _, err := extract(tarball(t, entry{name: "one/big", body: "0123456789"}), t.TempDir(), 5); err == nil {
		t.Fatal("an entry over the cap was accepted")
	}
}

func TestExtractDropsLinks(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "one/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "one/ok", Typeflag: tar.TypeReg, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := extractTarGz(&buf, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "link")); err == nil {
		t.Fatal("a symlink was written")
	}
	if _, err := os.Stat(filepath.Join(dir, "ok")); err != nil {
		t.Fatal("the regular file is missing")
	}
}

func TestDownloadAppliesTheSameFilters(t *testing.T) {
	sources := []string{source, "https://gitlab.com/acme/skills", "https://github.com/acme"}
	cfg := Config{Parent: t.TempDir(), Sources: sources, Check: public}
	ctx := context.Background()
	for name, url := range map[string]string{
		"not listed":    "https://github.com/other/repo",
		"not github":    "https://gitlab.com/acme/skills",
		"no repository": "https://github.com/acme",
	} {
		if _, err := NewGate().Download(ctx, cfg, http.DefaultClient, url, "main"); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := NewGate().Download(ctx, cfg, nil, source, "main"); err == nil || errors.Is(err, ErrRefused) {
		t.Errorf("a missing client: %v", err)
	}
}

func TestSplitRepo(t *testing.T) {
	for raw, want := range map[string]string{
		"/acme/skills":       "acme/skills",
		"/acme/skills.git/":  "acme/skills",
		"/acme":              "",
		"/acme/skills/extra": "",
		"/ac me/skills":      "",
	} {
		owner, repo, ok := splitRepo(raw)
		got := ""
		if ok {
			got = owner + "/" + repo
		}
		if got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}
