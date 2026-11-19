package rtk

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

const diffFixture = `diff --git a/foo.go b/foo.go
index 1111111..2222222 100644
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 package foo
-var a = 1
+var a = 2
+var b = 3
`

const diffWant = `
foo.go
  @@ -1,3 +1,4 @@
  -var a = 1
  +var a = 2
  +var b = 3
  +2 -1`

const statusFixture = "## main...origin/main\n M a.go\n?? b.txt\n"

const statusWant = "* main...origin/main\n~ Modified: 1 files\n   a.go\n? Untracked: 1 files\n   b.txt"

const logFixture = `commit 1234567890abcdef1234567890abcdef12345678
Author: A <a@b.c>
Date:   Mon Jan 1 00:00:00 2024 +0000

    Fix the thing

 some body text
diff --git a/x b/x
`

const logWant = "commit 1234567890abcdef1234567890abcdef12345678\nAuthor: A <a@b.c>\nDate:   Mon Jan 1 00:00:00 2024 +0000\n  Subject: Fix the thing\n  ... diff body omitted"

const grepFixture = "app/a.go:10:func a() {}\napp/a.go:12:func b() {}\napp/b.go:3:   x\n"

const grepWant = "3 matches in 2F:\n\n[file] app/a.go (2):\n    10: func a() {}\n    12: func b() {}\n\n[file] app/b.go (1):\n     3: x\n\n"

const findFixture = "app/a.go\napp/b.go\nlib/c.go\n"

const findWant = "3 files in 2 dirs:\n\napp/  (2)\n  a.go\n  b.go\nlib/  (1)\n  c.go\n"

const lsFixture = "total 8\ndrwxr-xr-x 2 me me 4096 Jan  1 12:00 src\n-rw-r--r-- 1 me me  123 Jan  1 12:00 a.go\n"

const lsWant = "src/\na.go  123B\n\nSummary: 1 files, 1 dirs (1 .go)"

const treeFixture = ".\n├── a\n└── b\n\n2 directories, 0 files\n"

const treeWant = ".\n├── a\n└── b"

const searchFixture = "Result of search in 'src' (total 2 files):\n- src/a.go\n- src/b.go\n"

const searchWant = "Result of search in 'src' (total 2 files):\n2 files in 1 dirs:\n\nsrc/ (2):\n  a.go\n  b.go"

const buildFixture = "npm warn deprecated foo@1.0.0: use bar\nadded 2 packages\n"

const buildWant = "npm warn deprecated foo@1.0.0: use bar\nadded 2 packages"

func TestEachFilterKeepsTheShapeOfItsTool(t *testing.T) {
	for name, c := range map[string]struct{ in, want string }{
		"git-diff":     {diffFixture, diffWant},
		"git-status":   {statusFixture, statusWant},
		"git-log":      {logFixture, logWant},
		"grep":         {grepFixture, grepWant},
		"find":         {findFixture, findWant},
		"ls":           {lsFixture, lsWant},
		"tree":         {treeFixture, treeWant},
		"search-list":  {searchFixture, searchWant},
		"build-output": {buildFixture, buildWant},
	} {
		f := byName(name)
		if f == nil {
			t.Fatalf("%s: no such filter", name)
		}
		if got := f.fn(c.in); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", name, got, c.want)
		}
	}
}

func TestTheDetectorPicksTheMostSpecificShape(t *testing.T) {
	cases := map[string]string{
		diffFixture: "git-diff",
		"commit 1234567890abcdef1234567890abcdef12345678\nAuthor: A <a@b.c>\n": "git-log",
		statusFixture:                     "git-status",
		buildFixture:                      "build-output",
		grepFixture:                       "grep",
		findFixture:                       "find",
		treeFixture:                       "tree",
		lsFixture:                         "ls",
		searchFixture:                     "search-list",
		strings.Repeat("noise line\n", 8): "dedup-log",
	}
	for in, want := range cases {
		f := detect(in)
		if f == nil || f.name != want {
			got := "<none>"
			if f != nil {
				got = f.name
			}
			t.Errorf("%.24q: %s, want %s", in, got, want)
		}
	}
}

func TestPlainProseIsNotCompressed(t *testing.T) {
	for _, in := range []string{
		"Could you write a short summary of this proposal?",
		"line one\nline two\nline three",
		strings.Repeat("a", 400),
	} {
		if f := detect(in); f != nil {
			t.Errorf("%.24q: detected %s", in, f.name)
		}
		if out, name := Compress(in); out != in || name != "" {
			t.Errorf("%.24q: compress changed it via %q", in, name)
		}
	}
}

func TestTheGenericFiltersKeepHeadAndTail(t *testing.T) {
	var numbered strings.Builder
	for i := 1; i <= 300; i++ {
		if i > 1 {
			numbered.WriteString("\n")
		}
		fmt.Fprintf(&numbered, "  %d|line %d", i, i)
	}
	out := readNumbered(numbered.String())
	if !strings.HasPrefix(out, "  1|line 1\n") || !strings.HasSuffix(out, "  300|line 300") {
		t.Fatalf("edges lost: %.40q ... %.40q", out, out)
	}
	if !strings.Contains(out, "... +120 lines truncated (file continues)") {
		t.Fatalf("no cut notice: %q", out)
	}

	var plain strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&plain, "line %d\n", i)
	}
	if out := smartTruncate(strings.TrimSuffix(plain.String(), "\n")); !strings.Contains(out, "... +120 lines truncated") {
		t.Fatalf("no cut notice: %.40q", out)
	}

	if out := dedupLog("a\na\na\nb\n\n\n\nc\n"); out != "a\n  ... (2 duplicate lines)\nb\n\nc\n" {
		t.Fatalf("dedup %q", out)
	}
}

func TestATinyBlockIsLeftAlone(t *testing.T) {
	small := "diff --git a/a b/a\n@@ -1 +1 @@\n-old\n+new\n"
	if len(small) >= minCompress {
		t.Fatalf("the fixture is %d bytes", len(small))
	}
	if f := detect(small); f == nil {
		t.Fatal("the detector no longer sees a diff")
	}
	if out, name := Compress(small); out != small || name != "" {
		t.Fatalf("a %d-byte block was rewritten by %q", len(small), name)
	}
}

func TestTheHeadWindowStaysOnARuneBoundary(t *testing.T) {
	long := strings.Repeat("é", 3000)
	got := cutUTF8(long, 4097)
	if len(got) > 4097 || !utf8.ValidString(got) {
		t.Fatalf("cut to %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
	if got := cutUTF8(long, 2*len(long)); got != long {
		t.Fatal("a short enough string changed")
	}
}

func TestACompressionMustBeShorterAndNonEmpty(t *testing.T) {
	var big strings.Builder
	big.WriteString(diffFixture)
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&big, "-old line %d\n+new line %d\n", i, i)
	}
	out, name := Compress(big.String())
	if name != "git-diff" {
		t.Fatalf("filter %q", name)
	}
	if out == "" || len(out) >= big.Len() {
		t.Fatalf("accepted a %d-byte answer for %d bytes", len(out), big.Len())
	}
	if out2, name2 := Compress("x"); out2 != "x" || name2 != "" {
		t.Fatal("a tiny block was rewritten")
	}
}

func FuzzCompressKeepsTheContract(f *testing.F) {
	f.Add(diffFixture)
	f.Add(statusFixture)
	f.Add(grepFixture)
	f.Add(findFixture)
	f.Add(strings.Repeat("x", 600))
	f.Fuzz(func(t *testing.T, in string) {
		out, name := Compress(in)
		if out == "" && in != "" {
			t.Fatalf("emptied %q", in)
		}
		if len(out) > len(in) {
			t.Fatalf("%s grew %d to %d", name, len(in), len(out))
		}
		// A block is only ever passed through or rewritten at ASCII boundaries, so
		// valid text stays valid. Invalid bytes are the caller's, not ours.
		if utf8.ValidString(in) && !utf8.ValidString(out) {
			t.Fatalf("%s made valid text invalid", name)
		}
		if name != "" && len(out) >= len(in) {
			t.Fatalf("%s was accepted but did not shrink", name)
		}
	})
}
