package health

import (
	"sync"
	"testing"
	"time"
)

// clock is a hand-cranked time source, so the breaker's window is exact.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock { return &clock{at: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func fail(t *testing.T, tr *Tracker, provider string, n int) {
	t.Helper()
	for range n {
		tr.Record(provider, false, 0)
	}
}

func TestTheFifthFailureInARowOpensTheBreaker(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.Now)
	// Four, not "one below the threshold": the rule is four, and a constant the
	// test derives from itself would pass whatever the constant became.
	fail(t, tr, "openai", 4)
	if tr.Open("openai") {
		t.Fatal("four failures opened the breaker")
	}
	if got := tr.Snapshot()["openai"]; got.CircuitState != Closed {
		t.Fatalf("state %q after four failures", got.CircuitState)
	}
	fail(t, tr, "openai", 1)
	if !tr.Open("openai") {
		t.Fatal("the fifth failure left the breaker closed")
	}
	if got := tr.Snapshot()["openai"]; got.CircuitState != Open {
		t.Fatalf("state %q", got.CircuitState)
	}
}

func TestASuccessBetweenFailuresKeepsTheBreakerClosed(t *testing.T) {
	tr := NewTracker(newClock().Now)
	fail(t, tr, "openai", 4)
	tr.Record("openai", true, 100*time.Millisecond)
	fail(t, tr, "openai", 4)
	if tr.Open("openai") {
		t.Fatal("failures separated by a success opened the breaker")
	}
}

func TestTheBreakerWaitsSixtySecondsThenAllowsOneTrial(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.Now)
	fail(t, tr, "openai", 5)
	c.Add(openFor - time.Second)
	if !tr.Open("openai") {
		t.Fatalf("open %s after opening", openFor-time.Second)
	}
	c.Add(time.Second)
	if tr.Open("openai") {
		t.Fatalf("still turned away at %s", openFor)
	}
	if got := tr.Snapshot()["openai"].CircuitState; got != HalfOpen {
		t.Fatalf("state %q", got)
	}
}

func TestASuccessClosesTheBreakerFromAnyState(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.Now)
	fail(t, tr, "openai", 5)
	tr.Record("openai", true, 10*time.Millisecond)
	if tr.Open("openai") {
		t.Fatal("a success while open left the breaker open")
	}
	if got := tr.Snapshot()["openai"].CircuitState; got != Closed {
		t.Fatalf("state %q", got)
	}
}

func TestAFailedHalfOpenTrialStartsTheWaitAgain(t *testing.T) {
	c := newClock()
	tr := NewTracker(c.Now)
	fail(t, tr, "openai", 5)
	c.Add(openFor)
	if tr.Open("openai") {
		t.Fatal("not half-open")
	}
	tr.Record("openai", false, 0)
	if !tr.Open("openai") {
		t.Fatal("a failed trial left the breaker half-open")
	}
	c.Add(openFor - time.Second)
	if !tr.Open("openai") {
		t.Fatal("the wait was measured from the first opening, not the trial")
	}
	c.Add(time.Second)
	if tr.Open("openai") {
		t.Fatal("the second wait did not end")
	}
}

func TestLatencyIsASmoothedAverageOfSuccessesOnly(t *testing.T) {
	tr := NewTracker(newClock().Now)
	tr.Record("openai", true, 100*time.Millisecond)
	if got := tr.Snapshot()["openai"].EMALatency; got != 100 {
		t.Fatalf("first sample %d", got)
	}
	tr.Record("openai", true, 200*time.Millisecond)
	if got := tr.Snapshot()["openai"].EMALatency; got != 120 {
		t.Fatalf("second sample %d, want 120", got)
	}
	tr.Record("openai", false, 5*time.Second)
	if got := tr.Snapshot()["openai"].EMALatency; got != 120 {
		t.Fatalf("a failure moved the average to %d", got)
	}
}

func TestTheCountersDecaySoTheRateStaysAboutToday(t *testing.T) {
	tr := NewTracker(newClock().Now)
	tr.Record("openai", true, time.Millisecond)
	fail(t, tr, "openai", 200)
	got := tr.Snapshot()["openai"]
	if got.FailCount > decayAt+1 {
		t.Fatalf("%d failures never decayed", got.FailCount)
	}
	if got.SuccessRate == nil || *got.SuccessRate > 0.02 {
		t.Fatalf("rate %v after 200 failures against 1 success", got.SuccessRate)
	}
}

func TestAnUnknownProviderIsClosedAndRateLess(t *testing.T) {
	tr := NewTracker(newClock().Now)
	if tr.Open("nobody") {
		t.Fatal("an unknown provider is turned away")
	}
	if _, ok := tr.Snapshot()["nobody"]; ok {
		t.Fatal("an unknown provider has a state")
	}
	var nilTracker *Tracker
	if nilTracker.Open("openai") {
		t.Fatal("a nil tracker turns traffic away")
	}
	if len(nilTracker.Snapshot()) != 0 {
		t.Fatal("a nil tracker has a snapshot")
	}
	nilTracker.Record("openai", true, 0)
}

func TestRecordingIsSafeFromManyGoroutines(t *testing.T) {
	tr := NewTracker(newClock().Now)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			for j := range 50 {
				tr.Record("openai", (i+j)%7 != 0, time.Duration(j)*time.Millisecond)
				tr.Open("openai")
				tr.Snapshot()
			}
		})
	}
	wg.Wait()
}
