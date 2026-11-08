package analytics

import (
	"context"
	"testing"
	"time"

	"rllmx/tests"
)

func TestPruneRemovesOldRowsAndKeepsDailyRollups(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seed(t, s, 30, now.AddDate(0, 0, -120), time.Hour)
	seed(t, s, 30, now.AddDate(0, 0, -10), time.Hour)
	dailyBefore := count(t, s, "rollupDaily")

	n, err := s.Prune(context.Background(), now, 90)
	if err != nil {
		t.Fatal(err)
	}
	if n != 30 {
		t.Fatalf("pruned %d requests, want 30", n)
	}
	if got := count(t, s, "requests"); got != 30 {
		t.Fatalf("requests left = %d", got)
	}
	var oldHourly []int64
	if err := s.db.Select(&oldHourly, `SELECT count(*) FROM "rollupHourly" WHERE "bucket" < ?`, hourBucket(now.AddDate(0, 0, -90))); err != nil {
		t.Fatal(err)
	}
	if oldHourly[0] != 0 {
		t.Fatalf("%d hourly rollups older than the window survived", oldHourly[0])
	}
	if got := count(t, s, "rollupDaily"); got != dailyBefore {
		t.Fatalf("daily rollups changed: %d -> %d", dailyBefore, got)
	}
}

func TestPruneBoundaryIsExclusive(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	cutoff := now.AddDate(0, 0, -90)
	w := s.NewWriter(WriterOptions{})
	w.Enqueue(Record{Time: cutoff.Add(-time.Millisecond), Provider: "p", Model: "old"})
	w.Enqueue(Record{Time: cutoff, Provider: "p", Model: "edge"})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	n, err := s.Prune(context.Background(), now, 90)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1 (only the row strictly older than the cutoff)", n)
	}
}

func TestPruneInBatches(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seed(t, s, 250, now.AddDate(0, 0, -200), time.Minute)
	s.batch = 100
	n, err := s.Prune(context.Background(), now, 90)
	if err != nil || n != 250 {
		t.Fatalf("pruned %d %v", n, err)
	}
}

func TestPruneWithNoRetentionKeepsEverything(t *testing.T) {
	s := newStore(t)
	seed(t, s, 5, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Hour)
	n, err := s.Prune(context.Background(), time.Now(), 0)
	if err != nil || n != 0 || count(t, s, "requests") != 5 {
		t.Fatalf("pruned %d %v", n, err)
	}
}

func TestIncrementalVacuumShrinksTheFileAfterPrune(t *testing.T) {
	tests.RequireSQLite(t)
	s := newStore(t)
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	seed(t, s, 20000, now.AddDate(0, 0, -200), time.Second)
	ctx := context.Background()
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	full, err := sizeOf(s.path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Prune(ctx, now, 90); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.IncrementalVacuum(ctx, 1<<30); err != nil {
		t.Fatal(err)
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	shrunk, err := sizeOf(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if shrunk >= full/2 {
		t.Fatalf("file went from %d to %d bytes; incremental vacuum freed nothing", full, shrunk)
	}
}

func TestIncrementalVacuumOnAnEmptyFreelistIsHarmless(t *testing.T) {
	s := newStore(t)
	if err := s.IncrementalVacuum(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeIsANoOpOnSQLite(t *testing.T) {
	s := newStore(t)
	if err := s.Analyze(context.Background()); err != nil {
		t.Fatal(err)
	}
}
