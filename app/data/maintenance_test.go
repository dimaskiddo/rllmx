package data

import (
	"context"
	"rllmx/app/dialect"
	"testing"
	"time"

	"rllmx/app/analytics"
	"rllmx/app/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

func TestTheMainStoreJobsAreOptimizeAndCheckpointEverySixHours(t *testing.T) {
	jobs := MainJobs(func() *repository.Store { return store.Migrated(t).Store })
	names := map[string]Job{}
	for _, j := range jobs {
		names[j.Name] = j
		if j.Cron == "" || j.Run == nil || j.Offset <= 0 {
			t.Errorf("job %s is incomplete: %+v", j.Name, j)
		}
	}
	for _, want := range []string{"optimize", "checkpoint"} {
		if _, ok := names[want]; !ok {
			t.Errorf("job %s missing", want)
		}
	}
	if names["optimize"].Cron != names["checkpoint"].Cron || names["optimize"].Offset == names["checkpoint"].Offset {
		t.Errorf("the two jobs share a schedule but must not start together: %+v %+v", names["optimize"], names["checkpoint"])
	}
}

func TestTheMainStoreJobsRunAndLeaveTheDataAlone(t *testing.T) {
	s := store.Migrated(t)
	if err := s.Settings().Patch(context.Background(), map[string]string{"marker": `"present"`}); err != nil {
		t.Fatal(err)
	}
	for _, j := range MainJobs(func() *repository.Store { return s.Store }) {
		if err := j.Run(context.Background()); err != nil {
			t.Errorf("%s: %v", j.Name, err)
		}
	}
	doc, err := s.Settings().Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := doc.GetString("marker"); v != "present" {
		t.Fatalf("maintenance changed the data: %s", doc.String())
	}
}

func TestAMainJobStopsWhenItsContextEnds(t *testing.T) {
	s := store.Migrated(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, j := range MainJobs(func() *repository.Store { return s.Store }) {
		if err := j.Run(ctx); err == nil {
			t.Errorf("%s ran on a cancelled context", j.Name)
		}
	}
}

func TestTheMainJobsDoNothingBeforeTheStoreIsOpen(t *testing.T) {
	for _, j := range MainJobs(func() *repository.Store { return nil }) {
		if err := j.Run(context.Background()); err != nil {
			t.Errorf("%s with no store: %v", j.Name, err)
		}
	}
}

func newAnalytics(t *testing.T) *analytics.Store {
	t.Helper()
	sq := tests.NewSQLite(t)
	s, err := analytics.Open(context.Background(), analytics.Options{
		DB: sq.DB, Schema: sq.Schema, Log: sq.Log, Dialect: dialect.SQLite,
		File: sq.File, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func TestTheAnalyticsJobsCoverBothSchedulesAndStartApart(t *testing.T) {
	s := newAnalytics(t)
	jobs := AnalyticsJobs(func() *analytics.Store { return s }, func() int { return 90 })
	names := map[string]Job{}
	offsets := map[time.Duration]bool{}
	for _, j := range jobs {
		names[j.Name] = j
		if j.Cron == "" || j.Run == nil || j.Offset <= 0 {
			t.Errorf("job %s is incomplete: %+v", j.Name, j)
		}
		if offsets[j.Offset] {
			t.Errorf("two jobs start at the same offset: %s", j.Offset)
		}
		offsets[j.Offset] = true
	}
	for _, want := range []string{"analytics-optimize", "analytics-checkpoint", "analytics-prune", "analytics-analyze"} {
		if _, ok := names[want]; !ok {
			t.Errorf("job %s missing", want)
		}
	}
	if names["analytics-prune"].Cron != names["analytics-analyze"].Cron {
		t.Error("prune and analyze belong on the daily schedule")
	}
	if names["analytics-optimize"].Cron != sixHours || names["analytics-checkpoint"].Cron != sixHours {
		t.Error("optimize and checkpoint belong on the six-hour schedule")
	}
}

func TestTheAnalyticsJobsRunOnAnOpenStore(t *testing.T) {
	s := newAnalytics(t)
	w := s.NewWriter(analytics.WriterOptions{})
	w.Enqueue(analytics.Record{Model: "m"})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, j := range AnalyticsJobs(func() *analytics.Store { return s }, func() int { return 90 }) {
		if err := j.Run(context.Background()); err != nil {
			t.Errorf("%s: %v", j.Name, err)
		}
	}
}

func TestThePruneJobReadsItsRetentionWhenItRuns(t *testing.T) {
	s := newAnalytics(t)
	days := 90
	jobs := AnalyticsJobs(func() *analytics.Store { return s }, func() int { return days })
	days = 0
	for _, j := range jobs {
		if j.Name != "analytics-prune" {
			continue
		}
		if err := j.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	days = 90
	for _, j := range jobs {
		if j.Name == "analytics-prune" {
			if err := j.Run(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestAnAnalyticsJobStopsWhenItsContextEnds(t *testing.T) {
	s := newAnalytics(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, j := range AnalyticsJobs(func() *analytics.Store { return s }, func() int { return 90 }) {
		if err := j.Run(ctx); err == nil {
			t.Errorf("%s ran on a cancelled context", j.Name)
		}
	}
}

func TestTheAnalyticsJobsDoNothingBeforeTheStoreIsOpen(t *testing.T) {
	for _, j := range AnalyticsJobs(func() *analytics.Store { return nil }, func() int { return 90 }) {
		if err := j.Run(context.Background()); err != nil {
			t.Errorf("%s with no store: %v", j.Name, err)
		}
	}
}
