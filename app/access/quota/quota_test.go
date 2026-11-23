package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"rllmx/app/store/analytics"
	"rllmx/app/store/repository"
	"rllmx/tests/store"
)

var now = time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC)

func amounts(day, month, total float64) *Amounts {
	return &Amounts{Day: day, Month: month, Total: total}
}

// source is a budget table a test writes by hand.
type source map[string]Limit

func (s source) All(context.Context) (map[string]Limit, error) { return s, nil }

type brokenSource struct{ err error }

func (b brokenSource) All(context.Context) (map[string]Limit, error) { return nil, b.err }

func TestAValidBudgetIsAcceptedAndAnInvalidOneIsRefusedByName(t *testing.T) {
	ok := []string{
		`{}`,
		`{"requests":{"day":1000}}`,
		`{"requests":{"day":1,"month":2,"total":3},"tokens":{"month":2000000},"cost":{"total":25.5}}`,
		`{"requests":{"day":0}}`,
	}
	for _, raw := range ok {
		if _, err := Parse(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	bad := map[string]string{
		"not an object":      `[1]`,
		"an unknown type":    `{"images":{"day":1}}`,
		"an unknown window":  `{"requests":{"week":1}}`,
		"a negative day":     `{"requests":{"day":-1}}`,
		"a fractional count": `{"requests":{"day":1.5}}`,
		"a string amount":    `{"cost":{"day":"1"}}`,
		"an object amount":   `{"cost":{"day":{}}}`,
		"an absurd amount":   `{"tokens":{"day":1e20}}`,
		"a type not object":  `{"requests":5}`,
	}
	for name, raw := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestAnEmptyBudgetClearsEveryType(t *testing.T) {
	l, err := Parse(`{"requests":{},"tokens":{"day":0}}`)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Empty() {
		t.Fatalf("%+v", l)
	}
}

// check runs one check with a stored and pending usage.
func check(t *testing.T, budget Limit, stored, pending analytics.Totals) (Refusal, int) {
	t.Helper()
	queries := 0
	q := &Quota{
		Limits: source{"k": budget},
		Usage: func(_ context.Context, _ string, _ time.Time) (analytics.Totals, error) {
			queries++
			return stored, nil
		},
		Pending: func(string) analytics.Totals { return pending },
		Now:     func() time.Time { return now },
	}
	r, err := q.Check(context.Background(), "k")
	if err != nil {
		t.Fatal(err)
	}
	return r, queries
}

func TestAKeyUnderItsBudgetSpends(t *testing.T) {
	budget := Limit{Requests: amounts(10, 0, 0)}
	if r, _ := check(t, budget, analytics.Totals{Requests: 9}, analytics.Totals{}); r.Refused {
		t.Fatalf("%+v", r)
	}
}

func TestAKeyAtItsDayBudgetIsRefusedUntilMidnight(t *testing.T) {
	budget := Limit{Requests: amounts(10, 0, 0)}
	r, _ := check(t, budget, analytics.Totals{Requests: 10}, analytics.Totals{})
	if !r.Refused {
		t.Fatal("a spent day budget was not refused")
	}
	// 10:30 until midnight is 13h30m.
	if r.RetryAfter != 13*time.Hour+30*time.Minute {
		t.Fatalf("retry after %s", r.RetryAfter)
	}
	if r.Remaining.Requests != 0 || r.Remaining.Tokens != -1 || r.Remaining.Cost != -1 {
		t.Fatalf("remaining %+v", r.Remaining)
	}
}

func TestAQueuedRowCountsBeforeItIsFlushed(t *testing.T) {
	budget := Limit{Requests: amounts(10, 0, 0)}
	r, _ := check(t, budget, analytics.Totals{Requests: 9}, analytics.Totals{Requests: 1})
	if !r.Refused {
		t.Fatal("the queued row did not count")
	}
}

func TestTheMonthAndTotalBudgetsAreSeparate(t *testing.T) {
	// The day is untouched; the month is spent.
	budget := Limit{Tokens: amounts(100, 50, 0)}
	r, _ := check(t, budget, analytics.Totals{Tokens: 60}, analytics.Totals{})
	if !r.Refused || r.RetryAfter <= 0 || r.RetryAfter > 31*24*time.Hour {
		t.Fatalf("%+v", r)
	}
	// A total budget never resets.
	total := Limit{Cost: amounts(0, 0, 5)}
	r, _ = check(t, total, analytics.Totals{Cost: 5}, analytics.Totals{})
	if !r.Refused || r.RetryAfter != 0 {
		t.Fatalf("%+v", r)
	}
	if r.Remaining.Cost != 0 || r.Remaining.Requests != -1 {
		t.Fatalf("remaining %+v", r.Remaining)
	}
}

func TestTheBudgetLeftIsReportedForTheWindowThatRefused(t *testing.T) {
	budget := Limit{Requests: amounts(0, 10, 0), Cost: amounts(0, 100, 0)}
	r, _ := check(t, budget, analytics.Totals{Requests: 10, Cost: 40}, analytics.Totals{})
	if !r.Refused || r.Remaining.Requests != 0 || r.Remaining.Cost != 60 {
		t.Fatalf("%+v", r)
	}
}

func TestAKeyWithoutABudgetOrIdIsNotChecked(t *testing.T) {
	queries := 0
	q := &Quota{
		Limits: source{},
		Usage: func(context.Context, string, time.Time) (analytics.Totals, error) {
			queries++
			return analytics.Totals{}, nil
		},
		Now: func() time.Time { return now },
	}
	if r, err := q.Check(context.Background(), "k"); err != nil || r.Refused {
		t.Fatalf("%+v %v", r, err)
	}
	if r, err := q.Check(context.Background(), ""); err != nil || r.Refused {
		t.Fatalf("an empty id: %+v %v", r, err)
	}
	if queries != 0 {
		t.Fatalf("%d usage queries for a key with no budget", queries)
	}
}

func TestAFailedReadFailsTheCheck(t *testing.T) {
	boom := errors.New("analytics is down")
	q := &Quota{
		Limits: brokenSource{err: boom},
		Usage:  func(context.Context, string, time.Time) (analytics.Totals, error) { return analytics.Totals{}, nil },
		Now:    func() time.Time { return now },
	}
	if _, err := q.Check(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	usageDown := &Quota{
		Limits: source{"k": Limit{Requests: amounts(1, 0, 0)}},
		Usage:  func(context.Context, string, time.Time) (analytics.Totals, error) { return analytics.Totals{}, boom },
		Now:    func() time.Time { return now },
	}
	if _, err := usageDown.Check(context.Background(), "k"); !errors.Is(err, boom) {
		t.Fatalf("a failed usage read err = %v", err)
	}
}

func TestTheWindowsFollowTheClocksZoneAndRollOver(t *testing.T) {
	jakarta := time.FixedZone("UTC+7", 7*3600)
	mid := time.Date(2026, 3, 14, 23, 59, 0, 0, jakarta)
	if got := windowStart("day", mid); !got.Equal(time.Date(2026, 3, 14, 0, 0, 0, 0, jakarta)) {
		t.Fatalf("day start = %s", got)
	}
	if got := untilWindowEnd("day", mid); got != time.Minute {
		t.Fatalf("until midnight = %s", got)
	}
	if got := windowStart("month", mid); !got.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, jakarta)) {
		t.Fatalf("month start = %s", got)
	}
	dec := time.Date(2026, 12, 20, 0, 0, 0, 0, jakarta)
	if got := untilWindowEnd("month", dec); got != 12*24*time.Hour {
		t.Fatalf("until the first = %s", got)
	}
	if got := windowStart("total", dec); !got.IsZero() {
		t.Fatalf("total start = %s", got)
	}
	if got := untilWindowEnd("total", dec); got != 0 {
		t.Fatalf("total end = %s", got)
	}
}

func TestABudgetRoundTripsThroughTheStore(t *testing.T) {
	st := store.Migrated(t)
	db := func() *repository.Store { return st.Store }
	l := NewLimits(db, func() time.Time { return now })
	ctx := context.Background()
	if err := l.Set(ctx, "k1", Limit{Requests: amounts(10, 0, 0), Cost: amounts(0, 0, 2.5)}); err != nil {
		t.Fatal(err)
	}
	all, err := l.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := all["k1"]
	if got.Requests == nil || got.Requests.Day != 10 || got.Cost == nil || got.Cost.Total != 2.5 || got.Tokens != nil {
		t.Fatalf("%+v", got)
	}
	// Clearing removes the row, so the next read has no limit for the key.
	if err := l.Clear(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	all, err = l.All(ctx)
	if err != nil || len(all) != 0 {
		t.Fatalf("%+v %v", all, err)
	}
}

func TestAStoredBudgetThatCannotBeReadFailsClosed(t *testing.T) {
	st := store.Migrated(t)
	ctx := context.Background()
	if err := st.KV().Set(ctx, repository.ScopeKeyLimits, "k1", `{"requests":{"week":1}}`); err != nil {
		t.Fatal(err)
	}
	l := NewLimits(func() *repository.Store { return st.Store }, func() time.Time { return now })
	if _, err := l.All(ctx); err == nil {
		t.Fatal("a corrupt budget was read as no budget")
	}
}
