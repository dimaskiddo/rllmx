package analytics

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeyTotalsSumsOneKeysDaysSinceAMoment(t *testing.T) {
	s := newStore(t)
	// Nine rows over three keys; k0 gets rows 0, 3 and 6.
	seed(t, s, 9, t0, time.Hour)
	all, err := s.KeyTotals(context.Background(), "k0", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Rows i = 0, 3, 6: requests 3; tokens (10+i)+(5+i) each; cost 0.001*(i+1).
	wantTokens := int64(15 + 21 + 27)
	wantCost := 0.001 * float64(1+4+7)
	if all.Requests != 3 || all.Tokens != wantTokens || diff(all.Cost, wantCost) > 1e-9 {
		t.Fatalf("%+v, want 3 / %d / %v", all, wantTokens, wantCost)
	}
	if other, _ := s.KeyTotals(context.Background(), "k1", time.Time{}); other.Requests != 3 {
		t.Fatalf("k1 = %+v", other)
	}
	if none, _ := s.KeyTotals(context.Background(), "nope", time.Time{}); none != (Totals{}) {
		t.Fatalf("an unknown key = %+v", none)
	}
}

func TestKeyTotalsHonoursTheWindowStart(t *testing.T) {
	s := newStore(t)
	seed(t, s, 6, t0, time.Hour)
	// t0 is 10:30, so the day bucket covers all six and "tomorrow" covers none.
	if got, _ := s.KeyTotals(context.Background(), "k0", t0.Add(24*time.Hour)); got.Requests != 0 {
		t.Fatalf("a future window = %+v", got)
	}
	if got, _ := s.KeyTotals(context.Background(), "k0", t0.Add(time.Minute)); got.Requests != 2 {
		t.Fatalf("a window from an hour in = %+v", got)
	}
}

func TestARequestTheProviderNeverServedDoesNotSpend(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{})
	failed := rec(0, t0)
	failed.Status, failed.ErrorClass = 500, "upstream_5xx"
	ok := rec(1, t0)
	ok.APIKeyID = failed.APIKeyID // one key, one failed and one served request
	if !w.Enqueue(failed) || !w.Enqueue(ok) {
		t.Fatal("enqueue refused")
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.KeyTotals(context.Background(), "k0", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Requests != 1 {
		t.Fatalf("requests = %d, want only the served one", got.Requests)
	}
}

func TestAPendingRowCountsBeforeItIsFlushed(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{FlushEvery: time.Hour, FlushRows: 1000})
	r := rec(0, t0)
	if !w.Enqueue(r) {
		t.Fatal("enqueue refused")
	}
	if got := w.Pending("k0"); got.Requests != 1 || got.Tokens != 15 || diff(got.Cost, 0.001) > 1e-9 {
		t.Fatalf("pending = %+v", got)
	}
	if stored, _ := s.KeyTotals(context.Background(), "k0", time.Time{}); stored.Requests != 0 {
		t.Fatalf("the row reached the database before the flush: %+v", stored)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := w.Pending("k0"); got != (Totals{}) {
		t.Fatalf("pending after a flush = %+v", got)
	}
	if stored, _ := s.KeyTotals(context.Background(), "k0", time.Time{}); stored.Requests != 1 {
		t.Fatalf("the row was not stored: %+v", stored)
	}
}

func TestADroppedRowNeverCounts(t *testing.T) {
	s := newStore(t)
	// One row is in flight (the flusher is held), one waits in the queue, and
	// everything after that is dropped because the queue holds one.
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w := s.NewWriter(WriterOptions{
		QueueSize: 1, FlushRows: 1, FlushEvery: time.Hour,
		beforeFlush: func() {
			once.Do(func() { close(started) })
			<-release
		},
	})
	if !w.Enqueue(sameKey(rec(0, t0))) {
		t.Fatal("the first row was refused")
	}
	<-started
	if !w.Enqueue(sameKey(rec(1, t0))) {
		t.Fatal("the queued row was refused")
	}
	for i := 2; i < 10; i++ {
		if w.Enqueue(sameKey(rec(i, t0))) {
			t.Fatalf("row %d was accepted into a full queue", i)
		}
	}
	if got := w.Pending("k0"); got.Requests != 2 {
		t.Fatalf("pending = %+v, want the in-flight and queued rows only", got)
	}
	close(release)
	_ = w.Close(context.Background())
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// sameKey puts a row on k0, which the other rows of a test use.
func sameKey(r Record) Record { r.APIKeyID = "k0"; return r }

func TestARowIsPendingBeforeTheFlusherCanSeeIt(t *testing.T) {
	// A row the flusher takes off the queue must already be counted, or its
	// removal runs first and the count it leaves behind is never taken off.
	// The flusher is parked inside the flush, so the check does not depend on
	// who runs first.
	s := newStore(t)
	var w *Writer
	var early atomic.Int64
	w = s.NewWriter(WriterOptions{QueueSize: 256, FlushRows: 1, FlushEvery: time.Hour, beforeFlush: func() {
		if w.Pending("k0").Requests < 1 {
			early.Add(1)
		}
	}})
	for i := 0; i < 2000; i++ {
		w.Enqueue(sameKey(rec(i, t0)))
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := early.Load(); n != 0 {
		t.Fatalf("%d rows reached the flusher before they were counted", n)
	}
	if got := w.Pending("k0"); got != (Totals{}) {
		t.Fatalf("a flushed queue still counts against the key: %+v", got)
	}
}
