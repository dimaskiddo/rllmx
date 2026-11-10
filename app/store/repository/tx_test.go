package repository

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"

	"rllmx/app/store/dialect"
	"rllmx/tests"
)

func TestConcurrentWritersSerialize(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 25 {
				err := s.Tx(ctx, func(tx contractsdb.Tx) error {
					return s.stmt(tx, `INSERT INTO "kv"("scope","key","value") VALUES ('t',?,?)`, dialect.NewID(), strconv.Itoa(w*100+i))
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
	if got := s.one(t, `SELECT count(*) FROM "kv" WHERE scope='t'`); got != "200" {
		t.Fatalf("%s rows, want 200", got)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := newMigrated(t)
	boom := errors.New("boom")
	err := s.Tx(context.Background(), func(tx contractsdb.Tx) error {
		_ = s.stmt(tx, `INSERT INTO "kv"("scope","key","value") VALUES ('r','k','v')`)
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if got := s.one(t, `SELECT count(*) FROM "kv" WHERE scope='r'`); got != "0" {
		t.Fatal("the transaction was not rolled back")
	}
}

// A failed callback must leave the pool usable, even though the caller's
// context stays alive (a long-lived server context never ends).
func TestTxReleasesTheWriterAfterAnError(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	_ = s.Tx(ctx, func(tx contractsdb.Tx) error {
		_ = s.stmt(tx, `INSERT INTO "kv"("scope","key","value") VALUES ('x','k','v')`)
		return errors.New("fail")
	})
	done := make(chan error, 1)
	go func() {
		done <- s.Tx(ctx, func(tx contractsdb.Tx) error {
			return s.stmt(tx, `INSERT INTO "kv"("scope","key","value") VALUES ('x','k2','v')`)
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write lock stayed held after a failed transaction")
	}
	if got := s.one(t, `SELECT count(*) FROM "kv" WHERE scope='x'`); got != "1" {
		t.Fatalf("%s rows, want only the second transaction's row", got)
	}
}

// The framework turns a panic in the callback into an error and rolls back, so
// the caller sees a failure rather than a crash, and the lock is released.
func TestTxReleasesTheWriterAfterAPanic(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	err := s.Tx(ctx, func(tx contractsdb.Tx) error {
		_ = s.stmt(tx, `INSERT INTO "kv"("scope","key","value") VALUES ('p','k','v')`)
		panic("boom")
	})
	if err == nil {
		t.Fatal("a panic inside the callback was swallowed")
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Tx(ctx, func(tx contractsdb.Tx) error {
			return s.stmt(tx, `INSERT INTO "kv"("scope","key","value") VALUES ('p','k2','v')`)
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the write lock stayed held after a panic")
	}
	if got := s.one(t, `SELECT count(*) FROM "kv" WHERE scope='p'`); got != "1" {
		t.Fatalf("%s rows, want only the second transaction's row", got)
	}
}

// Read-then-write inside Tx must not lose updates: 160 concurrent
// read-modify-write transactions each add one to a counter.
func TestReadModifyWriteInsideTxLosesNothing(t *testing.T) {
	s := newMigrated(t)
	ctx := context.Background()
	s.exec(t, `INSERT INTO "kv"("scope","key","value") VALUES ('c','n','0')`)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				err := s.Tx(ctx, func(tx contractsdb.Tx) error {
					v, _, err := s.kvGet(tx, "c", "n")
					if err != nil {
						return err
					}
					n, _ := strconv.Atoi(v)
					return s.kvPut(tx, "c", "n", strconv.Itoa(n+1))
				})
				if err != nil {
					t.Errorf("tx: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if got := s.one(t, `SELECT "value" FROM "kv" WHERE "scope"='c' AND "key"='n'`); got != "160" {
		t.Fatalf("counter = %s, want 160: updates were lost", got)
	}
}

// BEGIN IMMEDIATE is what protects against a second process (a deferred
// read-then-write would fail with SQLITE_BUSY at the write), so the test checks
// that the store holds the write lock from BEGIN, before it writes anything.
func TestWriterTransactionsStartImmediate(t *testing.T) {
	s := newMigrated(t)
	other := tests.OpenSQLiteWithQuery(t, s.SQ.File, "_pragma=busy_timeout(200)")
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Tx(context.Background(), func(tx contractsdb.Tx) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	err := other.DB.Statement(`INSERT INTO "kv"("scope","key","value") VALUES ('o','k','v')`)
	close(release)
	<-done
	if err == nil {
		t.Fatal("a second connection could write while the store held an open transaction: it did not take the write lock at BEGIN")
	}
}

func TestNewRefusesAnUnansweringDatabase(t *testing.T) {
	sq := tests.NewSQLite(t)
	if err := sq.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Dialect: dialect.SQLite}); err == nil {
		t.Fatal("New succeeded on a closed database")
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("New succeeded without a connection")
	}
}

func TestCountExpectsOneRow(t *testing.T) {
	s := newMigrated(t)
	if _, err := s.count(s.SQ.DB, `SELECT 1 UNION ALL SELECT 2`); err == nil {
		t.Fatal("a count query that returned two rows was accepted")
	}
	if n, err := s.count(s.SQ.DB, `SELECT count(*) FROM "kv"`); err != nil || n != 1 {
		t.Fatalf("count = %d, %v (the check row)", n, err)
	}
}
