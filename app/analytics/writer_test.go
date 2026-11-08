package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestWriterStoresRowsAndRollupsMatch(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	rng := rand.New(rand.NewSource(1))
	for i := range 700 {
		r := rec(i, t0.Add(time.Duration(rng.Intn(72*3600))*time.Second))
		if i%7 == 0 {
			r.Status, r.ErrorClass = 429, "rate_limit"
		}
		if !w.Enqueue(r) {
			t.Fatal("enqueue refused")
		}
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "requests"); got != 700 {
		t.Fatalf("requests = %d", got)
	}
	for _, table := range []string{"rollupHourly", "rollupDaily"} {
		var sums []struct {
			Requests   *int64   `db:"requests"`
			Errors     *int64   `db:"errors"`
			Prompt     *int64   `db:"prompt"`
			Completion *int64   `db:"completion"`
			Cached     *int64   `db:"cached"`
			CacheWrite *int64   `db:"cacheWrite"`
			Reasoning  *int64   `db:"reasoning"`
			Cost       *float64 `db:"cost"`
		}
		err := s.db.Select(&sums, `SELECT sum("requests") AS "requests", sum("errors") AS "errors", sum("promptTokens") AS "prompt", sum("completionTokens") AS "completion", sum("cachedTokens") AS "cached", sum("cacheWriteTokens") AS "cacheWrite", sum("reasoningTokens") AS "reasoning", sum("cost") AS "cost" FROM "`+table+`"`)
		if err != nil {
			t.Fatal(err)
		}
		var want []struct {
			Requests   int64   `db:"requests"`
			Prompt     int64   `db:"prompt"`
			Completion int64   `db:"completion"`
			Cached     int64   `db:"cached"`
			CacheWrite int64   `db:"cacheWrite"`
			Reasoning  int64   `db:"reasoning"`
			Cost       float64 `db:"cost"`
		}
		err = s.db.Select(&want, `SELECT count(*) AS "requests", sum("promptTokens") AS "prompt", sum("completionTokens") AS "completion", sum("cachedTokens") AS "cached", sum("cacheWriteTokens") AS "cacheWrite", sum("reasoningTokens") AS "reasoning", sum("cost") AS "cost" FROM "requests"`)
		if err != nil {
			t.Fatal(err)
		}
		var errors int64
		var e []int64
		if err := s.db.Select(&e, `SELECT sum(CASE WHEN "status" >= 400 THEN 1 ELSE 0 END) FROM "requests"`); err != nil {
			t.Fatal(err)
		}
		errors = e[0]
		a, b := sums[0], want[0]
		if deref(a.Requests) != b.Requests || deref(a.Errors) != errors || deref(a.Prompt) != b.Prompt ||
			deref(a.Completion) != b.Completion || deref(a.Cached) != b.Cached || deref(a.CacheWrite) != b.CacheWrite ||
			deref(a.Reasoning) != b.Reasoning || math.Abs(derefF(a.Cost)-b.Cost) > 1e-9 {
			t.Errorf("%s does not equal the sums over requests: %+v vs %+v", table, a, b)
		}
	}
	var buckets []int64
	if err := s.db.Select(&buckets, `SELECT count(DISTINCT "bucket") FROM "rollupHourly"`); err != nil {
		t.Fatal(err)
	}
	if buckets[0] < 24 {
		t.Errorf("only %d hourly buckets for a 72 hour spread", buckets[0])
	}
}

func deref(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func derefF(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func TestRollupKeysDifferingOnlyByCaseStayApart(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	w.Enqueue(Record{Time: t0, Provider: "p", Model: "GPT", Status: 200})
	w.Enqueue(Record{Time: t0, Provider: "p", Model: "gpt", Status: 200})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, "rollupDaily"); n != 2 {
		t.Fatalf("rollup rows = %d, want 2", n)
	}
}

func TestRollupBucketsAreUTC(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	loc := time.FixedZone("x", 14*3600)
	w.Enqueue(Record{Time: time.Date(2026, 3, 15, 1, 30, 0, 0, loc), Provider: "p", Model: "m", Status: 200})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	h, d := firstBucket(t, s, "rollupHourly"), firstBucket(t, s, "rollupDaily")
	wantH := time.Date(2026, 3, 14, 11, 0, 0, 0, time.UTC).UnixMilli()
	wantD := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC).UnixMilli()
	if h != wantH || d != wantD {
		t.Fatalf("buckets %d %d, want %d %d", h, d, wantH, wantD)
	}
}

func firstBucket(t testing.TB, s *testStore, table string) int64 {
	t.Helper()
	var v []int64
	if err := s.db.Select(&v, `SELECT "bucket" FROM "`+table+`"`); err != nil || len(v) == 0 {
		t.Fatalf("%s: %v", table, err)
	}
	return v[0]
}

func TestLifetimeCounterCountsEveryRow(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	for i := range 25 {
		w.Enqueue(rec(i, t0))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.lifetime(context.Background()); err != nil || n != 25 {
		t.Fatalf("lifetime = %d %v", n, err)
	}
}

func TestFullQueueDropsAndCounts(t *testing.T) {
	s := newStore(t)
	block := make(chan struct{})
	w := s.NewWriter(WriterOptions{QueueSize: 4, FlushRows: 1, FlushEvery: time.Hour, beforeFlush: func() { <-block }})
	accepted, refused := 0, 0
	for i := range 50 {
		if w.Enqueue(rec(i, t0)) {
			accepted++
		} else {
			refused++
		}
	}
	if refused == 0 || w.Dropped() != uint64(refused) {
		t.Fatalf("refused=%d dropped=%d", refused, w.Dropped())
	}
	close(block)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "requests"); got != int64(accepted) {
		t.Fatalf("stored %d, accepted %d", got, accepted)
	}
}

func TestEnqueueAfterCloseIsRefusedNotPanicking(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if w.Enqueue(rec(1, t0)) {
		t.Fatal("enqueue after close accepted")
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestCloseWhileManyGoroutinesEnqueue(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	var wg sync.WaitGroup
	var accepted atomic.Int64
	stop := make(chan struct{})
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				if w.Enqueue(rec(g*100000+i, t0)) {
					accepted.Add(1)
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	if got := count(t, s, "requests"); got != accepted.Load() {
		t.Fatalf("stored %d rows, %d were accepted", got, accepted.Load())
	}
}

func TestWriterFlushesOnTheInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newStore(t)
		w := s.NewWriter(WriterOptions{FlushRows: 1000, FlushEvery: time.Second})
		w.Enqueue(rec(1, t0))
		synctest.Wait()
		if got := count(t, s, "requests"); got != 0 {
			t.Fatalf("flushed before the interval: %d", got)
		}
		time.Sleep(1100 * time.Millisecond)
		synctest.Wait()
		if got := count(t, s, "requests"); got != 1 {
			t.Fatalf("not flushed after the interval: %d", got)
		}
		_ = w.Close(context.Background())
	})
}

func TestWriterFlushesWhenTheBatchIsFull(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{FlushRows: 10, FlushEvery: time.Hour})
	for i := range 10 {
		w.Enqueue(rec(i, t0))
	}
	if !waitFor(func() bool { return count(t, s, "requests") == 10 }) {
		t.Fatalf("batch of 10 not flushed: %d", count(t, s, "requests"))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func waitFor(cond func() bool) bool {
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
	return true
}

func TestFailedFlushDropsTheBatchAndKeepsRunning(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{FlushRows: 5, FlushEvery: time.Hour})
	if err := s.db.Statement(`ALTER TABLE "requests" RENAME TO "requests_gone"`); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		w.Enqueue(rec(i, t0))
	}
	if !waitFor(func() bool { return w.Failed() == 5 }) {
		t.Fatalf("failed rows = %d", w.Failed())
	}
	if err := s.db.Statement(`ALTER TABLE "requests_gone" RENAME TO "requests"`); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		w.Enqueue(rec(i, t0))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count(t, s, "requests"); got != 5 {
		t.Fatalf("writer did not recover: %d rows", got)
	}
}

func TestEnqueueNeverBlocksUnderLoad(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	const goroutines, each = 8, 12500
	var worst atomic.Int64
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				start := time.Now()
				w.Enqueue(rec(g*each+i, t0))
				if d := time.Since(start).Nanoseconds(); d > worst.Load() {
					worst.Store(d)
				}
			}
		}()
	}
	wg.Wait()
	if d := time.Duration(worst.Load()); d > 50*time.Millisecond {
		t.Fatalf("slowest Enqueue took %v", d)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLifetimeCounterAddsAcrossFlushes(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{FlushRows: 5, FlushEvery: time.Hour})
	for i := range 20 {
		w.Enqueue(rec(i, t0))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.lifetime(context.Background()); n != 20 {
		t.Fatalf("lifetime = %d after four flushes, want 20", n)
	}
}

func TestDayBucketIsUTCForAnyZone(t *testing.T) {
	loc := time.FixedZone("x", -11*3600)
	in := time.Date(2026, 3, 14, 20, 0, 0, 0, loc) // 2026-03-15 07:00 UTC
	if got, want := dayBucket(in), time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).UnixMilli(); got != want {
		t.Fatalf("dayBucket = %d, want %d", got, want)
	}
	half := time.Date(2026, 3, 14, 10, 20, 0, 0, time.FixedZone("y", 5*3600+1800))
	if got, want := hourBucket(half), time.Date(2026, 3, 14, 4, 0, 0, 0, time.UTC).UnixMilli(); got != want {
		t.Fatalf("hourBucket = %d, want %d", got, want)
	}
}

func TestCloseTimeoutStopsTheFlushBeforeReturning(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{FlushRows: 1, FlushEvery: time.Hour, beforeFlush: func() { time.Sleep(300 * time.Millisecond) }})
	w.Enqueue(rec(1, t0))
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := w.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-w.done:
	default:
		t.Fatal("Close returned while the flush goroutine could still write")
	}
	if got := count(t, s, "requests"); got != 0 {
		t.Fatalf("%d rows written after the close gave up", got)
	}
}

var _ = fmt.Sprint
