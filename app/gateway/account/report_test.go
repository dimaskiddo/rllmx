package account

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"rllmx/app/store/dialect"
)

// ctxWriter says whether the write it was handed could still run, and whether
// it carried a deadline of its own.
type ctxWriter struct {
	live    bool
	bounded bool
	calls   int
}

func (w *ctxWriter) LockFromLevel(ctx context.Context, id, model string, status int, reason string, decide func(int) (time.Time, int, bool)) (bool, error) {
	w.calls++
	_, w.bounded = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	w.live = true
	decide(0)
	return true, nil
}

func (w *ctxWriter) ClearLocks(ctx context.Context, id, model string, at, since time.Time) error {
	w.calls++
	_, w.bounded = ctx.Deadline()
	if err := ctx.Err(); err != nil {
		return err
	}
	w.live = true
	return nil
}

// The lock belongs to the account, not to the caller that has just gone: a
// request that gave up mid-write must still leave its account locked.
func TestAFailureIsRecordedEvenWhenTheCallerHasGone(t *testing.T) {
	w := &ctxWriter{}
	r := NewReporter(w, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := r.Fail(ctx, FailInput{ConnID: "c1", Provider: "p", Model: "m", Status: http.StatusTooManyRequests})
	if err != nil {
		t.Fatalf("the report failed on a cancelled caller: %v", err)
	}
	if !out.Fallback {
		t.Fatal("a failure the caller abandoned was not recorded")
	}
	if !w.live || !w.bounded {
		t.Fatalf("the write ran on a dead or unbounded context: live=%v bounded=%v", w.live, w.bounded)
	}
}

// A success clears the lock even when the answer is read after the caller left.
func TestASuccessIsClearedEvenWhenTheCallerHasGone(t *testing.T) {
	w := &ctxWriter{}
	r := NewReporter(w, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Succeed(ctx, "c1", "m", time.Now()); err != nil {
		t.Fatalf("clearing the locks failed on a cancelled caller: %v", err)
	}
	if !w.live || !w.bounded {
		t.Fatalf("the clear ran on a dead or unbounded context: live=%v bounded=%v", w.live, w.bounded)
	}
}

// A statement can spend the store's own busy wait queueing behind another
// writer before it starts. A budget shorter than that cuts a write the driver
// would have completed, and reports it as an interruption of our own making.
func TestTheReportBudgetOutlastsTheStoresBusyWait(t *testing.T) {
	const marker = "busy_timeout("
	raw := dialect.BusyWait
	_, after, ok := strings.Cut(raw, marker)
	if !ok {
		t.Fatalf("the store's busy wait is not in %q", raw)
	}
	ms, err := strconv.Atoi(strings.TrimSuffix(after, ")"))
	if err != nil {
		t.Fatalf("the store's busy wait is not a number: %v", err)
	}
	if wait := time.Duration(ms) * time.Millisecond; reportBudget < 2*wait {
		t.Fatalf("the report budget %s does not outlast the busy wait %s", reportBudget, wait)
	}
}
