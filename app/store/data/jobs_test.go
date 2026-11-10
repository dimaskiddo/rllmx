package data

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rllmx/tests"
)

func TestAJobRecordsItsStartEndAndResult(t *testing.T) {
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	clock := func() time.Time { now = now.Add(time.Second); return now }
	ran := 0
	j := NewJob(Job{Name: "x", Run: func(context.Context) error { ran++; return nil }}, context.Background(), &tests.Log{}, clock)
	if st := j.Status(); !st.LastStart.IsZero() || st.Runs != 0 {
		t.Fatalf("status before any run: %+v", st)
	}
	j.Run()
	st := j.Status()
	if ran != 1 || st.Runs != 1 || st.LastErr != "" || !st.LastEnd.After(st.LastStart) || st.LastStart.IsZero() {
		t.Fatalf("status = %+v ran=%d", st, ran)
	}
}

func TestAFailingJobIsRecordedLoggedAndNeverPanics(t *testing.T) {
	l := &tests.Log{}
	j := NewJob(Job{Name: "boom", Run: func(context.Context) error { return errors.New("disk full") }}, context.Background(), l, time.Now)
	j.Run()
	st := j.Status()
	if st.LastErr != "disk full" || st.Failures != 1 {
		t.Fatalf("status = %+v", st)
	}
	found := false
	for _, line := range l.Lines() {
		found = found || (strings.Contains(line, "boom") && strings.Contains(line, "disk full"))
	}
	if !found {
		t.Fatalf("the failure was not logged with the job name: %v", l.Lines())
	}
	j.Job.Run = func(context.Context) error { return nil }
	j.Run()
	if st := j.Status(); st.LastErr != "" || st.Failures != 1 || st.Runs != 2 {
		t.Fatalf("a later success must clear the error and keep the count: %+v", st)
	}
}

func TestAJobDoesNotStartAfterTheApplicationStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var ran atomic.Int32
	j := NewJob(Job{Name: "late", Run: func(context.Context) error { ran.Add(1); return nil }}, ctx, &tests.Log{}, time.Now)
	j.Run()
	if ran.Load() != 0 || j.Status().Runs != 0 {
		t.Fatal("a job started after the application context ended")
	}
}

func TestAJobStartsAfterItsOffset(t *testing.T) {
	var ran atomic.Int32
	j := NewJob(Job{Name: "off", Offset: 40 * time.Millisecond, Run: func(context.Context) error { ran.Add(1); return nil }}, context.Background(), &tests.Log{}, time.Now)
	begin := time.Now()
	j.Run()
	if time.Since(begin) < 40*time.Millisecond || ran.Load() != 1 {
		t.Fatalf("the job ran after %v (ran %d), want it to wait for its offset", time.Since(begin), ran.Load())
	}
}

func TestTheOffsetWaitEndsWhenTheApplicationStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int32
	j := NewJob(Job{Name: "off", Offset: time.Hour, Run: func(context.Context) error { ran.Add(1); return nil }}, ctx, &tests.Log{}, time.Now)
	done := make(chan struct{})
	go func() { j.Run(); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the job kept waiting for its offset after the application stopped")
	}
	if ran.Load() != 0 {
		t.Fatal("the job ran although the application stopped during its wait")
	}
}

func TestTheJobSetNamesEveryJobAndRunsTheStartupOnes(t *testing.T) {
	var calls = map[string]*atomic.Int32{}
	mk := func(name string) Job {
		c := &atomic.Int32{}
		calls[name] = c
		return Job{Name: name, Cron: "0 */6 * * *", Run: func(context.Context) error { c.Add(1); return nil }}
	}
	jobs := []Job{mk("optimize"), mk("checkpoint"), mk("prune")}
	set := NewJobSet(jobs, context.Background(), &tests.Log{}, time.Now)
	if len(set.Events()) != 3 {
		t.Fatalf("%d events", len(set.Events()))
	}
	for _, e := range set.Events() {
		if e.GetName() == "" || !e.GetSkipIfStillRunning() || e.GetCron() != "0 */6 * * *" {
			t.Errorf("event %q skip=%v cron=%q", e.GetName(), e.GetSkipIfStillRunning(), e.GetCron())
		}
	}
	set.RunStartup()
	for name, c := range calls {
		if c.Load() != 1 {
			t.Errorf("startup ran %s %d times", name, c.Load())
		}
	}
	if st, ok := set.Status("checkpoint"); !ok || st.Runs != 1 {
		t.Fatalf("status = %+v %v", st, ok)
	}
	if _, ok := set.Status("nope"); ok {
		t.Fatal("status of an unknown job")
	}
}

func TestRunStartupRunsEveryJobEvenWhenOneFails(t *testing.T) {
	var second atomic.Int32
	set := NewJobSet([]Job{
		{Name: "a", Cron: "* * * * *", Run: func(context.Context) error { return errors.New("x") }},
		{Name: "b", Cron: "* * * * *", Run: func(context.Context) error { second.Add(1); return nil }},
	}, context.Background(), &tests.Log{}, time.Now)
	set.RunStartup()
	if second.Load() != 1 {
		t.Fatal("a failing job stopped the others")
	}
}

func TestTheStartupRunDoesNotWaitForTheOffset(t *testing.T) {
	var ran atomic.Int32
	set := NewJobSet([]Job{{Name: "slow", Cron: "* * * * *", Offset: time.Hour, Run: func(context.Context) error { ran.Add(1); return nil }}}, context.Background(), &tests.Log{}, time.Now)
	done := make(chan struct{})
	go func() { set.RunStartup(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the startup run waited for the scheduled offset")
	}
	if ran.Load() != 1 {
		t.Fatalf("ran %d times", ran.Load())
	}
}
