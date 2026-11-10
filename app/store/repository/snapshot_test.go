package repository

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"rllmx/tests"
)

const walQuery = "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=wal_autocheckpoint(0)"

// writeSource creates a WAL-mode database and keeps its connection open until
// the test ends: closing it would checkpoint the WAL away.
func writeSource(t *testing.T, path string) *tests.Conn {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	sq := tests.OpenSQLiteWithQuery(t, path, walQuery)
	if err := sq.DB.Statement(`CREATE TABLE t (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"a", "b", "c"} {
		if err := sq.DB.Statement(`INSERT INTO t VALUES(?)`, v); err != nil {
			t.Fatal(err)
		}
	}
	return sq
}

func snapCount(t *testing.T, snap *Snapshot) int64 {
	t.Helper()
	var n []int64
	if err := snap.DB().Select(&n, `SELECT count(*) FROM t`); err != nil || len(n) != 1 {
		t.Fatalf("count: %v %v", n, err)
	}
	return n[0]
}

func TestSnapshotReadsRowsThatLiveOnlyInTheWAL(t *testing.T) {
	tests.RequireSQLite(t)
	src := filepath.Join(t.TempDir(), "src.sqlite")
	writeSource(t, src)
	if info, err := os.Stat(src + "-wal"); err != nil || info.Size() == 0 {
		t.Skipf("rows did not stay in the WAL: %v", err)
	}
	snap, err := OpenSnapshot(testOpener(t), src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if n := snapCount(t, snap); n != 3 {
		t.Fatalf("count = %d", n)
	}
}

func TestSnapshotIsOneConsistentFileNotACopyOfTheWALPieces(t *testing.T) {
	tests.RequireSQLite(t)
	src := filepath.Join(t.TempDir(), "src.sqlite")
	writeSource(t, src)
	snap, err := OpenSnapshot(testOpener(t), src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	entries, _ := os.ReadDir(snap.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-wal") {
			t.Fatalf("the snapshot carries a copied WAL: %s", e.Name())
		}
	}
	var res []string
	if err := snap.DB().Select(&res, `PRAGMA integrity_check`); err != nil || len(res) != 1 || res[0] != "ok" {
		t.Fatalf("integrity %v %v", res, err)
	}
}

func TestSnapshotNeverCheckpointsOrTouchesTheSourceWAL(t *testing.T) {
	tests.RequireSQLite(t)
	live := filepath.Join(t.TempDir(), "live.sqlite")
	writeSource(t, live)
	src := filepath.Join(t.TempDir(), "left.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		b, err := os.ReadFile(live + suffix)
		if err != nil {
			t.Skipf("no %s to copy: %v", suffix, err)
		}
		if err := os.WriteFile(src+suffix, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.Stat(src + "-wal")
	if err != nil || before.Size() == 0 {
		t.Skip("rows did not stay in the WAL")
	}
	snap, err := OpenSnapshot(testOpener(t), src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n := snapCount(t, snap); n != 3 {
		t.Fatalf("count = %d", n)
	}
	_ = snap.Close()
	after, err := os.Stat(src + "-wal")
	if err != nil || after.Size() != before.Size() {
		t.Fatalf("the source WAL was changed or removed: %v %v", after, err)
	}
}

func TestSnapshotFileIsOwnerOnly(t *testing.T) {
	tests.RequireSQLite(t)
	if runtime.GOOS == "windows" {
		t.Skip("no Unix modes")
	}
	src := filepath.Join(t.TempDir(), "src.sqlite")
	writeSource(t, src)
	snap, err := OpenSnapshot(testOpener(t), src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	info, err := os.Stat(filepath.Join(snap.dir, "source.sqlite"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot file: %v %v", info, err)
	}
}

func TestSnapshotOfADatabaseUnderWriteLoadIsIntact(t *testing.T) {
	tests.RequireSQLite(t)
	src := filepath.Join(t.TempDir(), "src.sqlite")
	sq := tests.OpenSQLiteWithQuery(t, src, "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err := sq.DB.Statement(`CREATE TABLE t (a INTEGER, b INTEGER)`); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = sq.DB.Statement(`INSERT INTO t VALUES(?, ?)`, i, i)
			if i%50 == 0 {
				_ = sq.DB.Statement(`PRAGMA wal_checkpoint(TRUNCATE)`)
			}
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	open := testOpener(t)
	for range 10 {
		snap, err := OpenSnapshot(open, src, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var res []string
		var bad []int64
		if err := snap.DB().Select(&res, `PRAGMA integrity_check`); err != nil || len(res) != 1 || res[0] != "ok" {
			t.Fatalf("integrity %v %v", res, err)
		}
		if err := snap.DB().Select(&bad, `SELECT count(*) FROM t WHERE a <> b`); err != nil || bad[0] != 0 {
			t.Fatalf("torn rows %v %v", bad, err)
		}
		_ = snap.Close()
	}
}

func TestSnapshotLeavesTheSourceAloneAndCleansUp(t *testing.T) {
	tests.RequireSQLite(t)
	src := filepath.Join(t.TempDir(), "src.sqlite")
	writeSource(t, src)
	read := func(p string) string {
		b, _ := os.ReadFile(p)
		return string(b)
	}
	before, beforeWAL := read(src), read(src+"-wal")
	work := filepath.Join(t.TempDir(), "import-tmp")
	snap, err := OpenSnapshot(testOpener(t), src, work)
	if err != nil {
		t.Fatal(err)
	}
	if err := snap.DB().Statement(`INSERT INTO t VALUES('x')`); err == nil {
		t.Fatal("the snapshot accepted a write")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(work); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("work dir: %v %v", info, err)
		}
	}
	if err := snap.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("%d temporary entries left", len(entries))
	}
	if read(src) != before || read(src+"-wal") != beforeWAL {
		t.Fatal("the source changed")
	}
	if err := snap.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestSnapshotOfAMissingOrForeignFileFails(t *testing.T) {
	tests.RequireSQLite(t)
	work := t.TempDir()
	open := testOpener(t)
	if _, err := OpenSnapshot(open, filepath.Join(t.TempDir(), "none.sqlite"), work); err == nil {
		t.Fatal("missing source accepted")
	}
	junk := filepath.Join(t.TempDir(), "junk.sqlite")
	if err := os.WriteFile(junk, []byte(strings.Repeat("not a database ", 20)), 0o600); err != nil {
		t.Fatal(err)
	}
	if snap, err := OpenSnapshot(open, junk, work); err == nil {
		_ = snap.Close()
		t.Fatal("a text file was accepted as a database")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("%d temporary entries left after a failure", len(entries))
	}
}

func TestSnapshotFailureAfterTheDirectoryWasMadeLeavesNothing(t *testing.T) {
	tests.RequireSQLite(t)
	work := filepath.Join(t.TempDir(), "w")
	if _, err := OpenSnapshot(testOpener(t), t.TempDir(), work); err == nil {
		t.Fatal("a directory was accepted as a database file")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("%d temporary entries left after the copy failed", len(entries))
	}
}

func TestResolveSourceDatabase(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "old", "db", "data.sqlite")
	flat := filepath.Join(root, "flat", "data.sqlite")
	for _, p := range []string{nested, flat} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ in, want string }{
		{filepath.Join(root, "old"), nested},
		{filepath.Join(root, "flat"), flat},
		{nested, nested},
	} {
		got, err := ResolveSourceDatabase(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("Resolve(%s) = %q %v, want %q", tc.in, got, err, tc.want)
		}
	}
	if _, err := ResolveSourceDatabase(root); err == nil {
		t.Error("a directory without a database accepted")
	}
	if _, err := ResolveSourceDatabase(filepath.Join(root, "absent")); err == nil {
		t.Error("a missing path accepted")
	}
}

func TestResolveSourceDatabasePrefersTheNestedLayout(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "db", "data.sqlite")
	if err := os.MkdirAll(filepath.Dir(nested), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.sqlite"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveSourceDatabase(root)
	if err != nil || got != nested {
		t.Fatalf("Resolve = %q %v, want the db/ copy", got, err)
	}
}

func TestSnapshotLeavesNothingWhenTheCopyCannotBeOpened(t *testing.T) {
	tests.RequireSQLite(t)
	src := filepath.Join(t.TempDir(), "src.sqlite")
	writeSource(t, src)
	real := testOpener(t)
	work := t.TempDir()
	opener := func(path, query string) (*Conn, error) {
		if strings.Contains(query, "query_only") {
			return nil, os.ErrPermission
		}
		return real(path, query)
	}
	if snap, err := OpenSnapshot(opener, src, work); err == nil {
		_ = snap.Close()
		t.Fatal("OpenSnapshot succeeded although the copy could not be opened")
	}
	if entries, _ := os.ReadDir(work); len(entries) != 0 {
		t.Fatalf("%d temporary entries left after the open failed", len(entries))
	}
}

func TestSnapshotClosesTheConnectionsItOpens(t *testing.T) {
	tests.RequireSQLite(t)
	src := filepath.Join(t.TempDir(), "src.sqlite")
	writeSource(t, src)
	real := testOpener(t)
	var mu sync.Mutex
	open := map[string]bool{}
	opener := func(path, query string) (*Conn, error) {
		c, err := real(path, query)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		open[path+"?"+query] = true
		mu.Unlock()
		inner := c.Close
		c.Close = func() error {
			mu.Lock()
			delete(open, path+"?"+query)
			mu.Unlock()
			return inner()
		}
		return c, nil
	}
	snap, err := OpenSnapshot(opener, src, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	during := len(open)
	mu.Unlock()
	if during != 1 {
		t.Fatalf("%d connections open while the snapshot is in use, want only the copy", during)
	}
	if err := snap.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(open) != 0 {
		t.Fatalf("%d connections left open after Close", len(open))
	}
}
