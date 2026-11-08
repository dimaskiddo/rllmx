package data

import (
	"context"
	"sync"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/goravel/framework/contracts/schedule"
	frameworkschedule "github.com/goravel/framework/schedule"
)

// Job is one piece of upkeep.
type Job struct {
	Name string
	// Cron is the schedule in the Schedule facade's format.
	Cron string
	// Offset delays each run, so jobs on one schedule do not start together. The
	// Schedule facade has no jitter of its own.
	Offset time.Duration
	Run    func(ctx context.Context) error
}

// Status is what the API reports about a job.
type Status struct {
	LastStart time.Time
	LastEnd   time.Time
	LastErr   string
	Runs      int
	Failures  int
}

// RunningJob wraps a Job with the application context, a clock and its status.
type RunningJob struct {
	Job Job
	ctx context.Context
	log log.Log
	now func() time.Time

	mu sync.Mutex
	st Status
}

// NewJob wraps j. The facade gives a job no context, so it is passed here and
// checked before every run; now is the clock, replaced in tests.
func NewJob(j Job, ctx context.Context, l log.Log, now func() time.Time) *RunningJob {
	return &RunningJob{Job: j, ctx: ctx, log: l, now: now}
}

// Run waits for the offset, runs the job once and records the outcome. It does
// nothing once the application context has ended, and a failure is logged and
// kept, never raised: upkeep never blocks the start or a request.
func (r *RunningJob) Run() {
	if r.Job.Offset > 0 {
		select {
		case <-time.After(r.Job.Offset):
		case <-r.ctx.Done():
			return
		}
	}
	r.runNow()
}

// runNow is Run without the offset, for the run at start.
func (r *RunningJob) runNow() {
	if r.ctx.Err() != nil {
		return
	}
	start := r.now()
	err := r.Job.Run(r.ctx)
	end := r.now()

	r.mu.Lock()
	r.st.LastStart, r.st.LastEnd = start, end
	r.st.Runs++
	r.st.LastErr = ""
	if err != nil {
		r.st.LastErr = err.Error()
		r.st.Failures++
	}
	r.mu.Unlock()
	if err != nil {
		r.log.Warningf("maintenance job %s failed: %v", r.Job.Name, err)
	}
}

// Status returns the last outcome.
func (r *RunningJob) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.st
}

// JobSet is the maintenance jobs registered with the Schedule facade.
type JobSet struct {
	jobs   []*RunningJob
	events []schedule.Event
}

// NewJobSet wraps each job and builds its named event, which skips a run while
// the last one is still going.
func NewJobSet(jobs []Job, ctx context.Context, l log.Log, now func() time.Time) *JobSet {
	set := &JobSet{}
	for _, j := range jobs {
		rj := NewJob(j, ctx, l, now)
		set.jobs = append(set.jobs, rj)
		set.events = append(set.events, frameworkschedule.NewCallbackEvent(rj.Run).Cron(j.Cron).Name(j.Name).SkipIfStillRunning())
	}
	return set
}

// Events returns the events for WithSchedule.
func (s *JobSet) Events() []schedule.Event { return s.events }

// RunStartup runs every job once, in order. The data runner calls it when the
// stores are open; a failure is recorded and the next job still runs.
func (s *JobSet) RunStartup() {
	for _, j := range s.jobs {
		j.runNow()
	}
}

// Status returns the status of a job by name.
func (s *JobSet) Status(name string) (Status, bool) {
	for _, j := range s.jobs {
		if j.Job.Name == name {
			return j.Status(), true
		}
	}
	return Status{}, false
}
