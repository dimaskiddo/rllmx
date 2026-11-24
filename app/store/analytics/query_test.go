package analytics

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func put(t testing.TB, s *testStore, recs ...Record) *Writer {
	t.Helper()
	w := s.NewWriter(WriterOptions{QueueSize: len(recs) + 1})
	for _, r := range recs {
		w.Enqueue(r)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

// usageRows seeds two days: one row on the first, two on the second.
func usageRows(t testing.TB, s *testStore) (dayA, dayB time.Time, w *Writer) {
	t.Helper()
	dayA = t0.Truncate(24 * time.Hour)
	dayB = dayA.Add(24 * time.Hour)
	mk := func(ts time.Time, provider, model string, prompt, completion int64, cost float64, status int) Record {
		return Record{
			Time: ts, RequestID: model + ts.Format("1504"), Provider: provider, Model: model,
			ConnectionID: "c-" + provider, APIKeyID: "k-" + provider, APIKeyName: "key",
			Endpoint: "/v1/chat/completions", Status: status, PromptTokens: prompt, CompletionTokens: completion, Cost: cost,
		}
	}
	w = put(t, s,
		mk(dayA.Add(time.Hour), "p1", "m1", 10, 5, 1.5, 200),
		mk(dayB.Add(time.Hour), "p1", "m1", 1, 2, 0.5, 500),
		mk(dayB.Add(2*time.Hour), "p2", "m2", 3, 4, 0, 200),
	)
	return dayA, dayB, w
}

func byKey(sums []Sum) map[Dims]Sum {
	out := map[Dims]Sum{}
	for _, s := range sums {
		out[s.Dims] = s
	}
	return out
}

func TestTheDailyRollupsAnswerAWindowWhoseHoursWerePruned(t *testing.T) {
	s := newStore(t)
	dayA, dayB, _ := usageRows(t, s)
	ctx := context.Background()

	// Retention removes the first day's hours and rows; the daily rollup stays.
	if err := s.db.WithContext(ctx).Statement(s.q(`DELETE FROM "rollupHourly" WHERE "bucket" < ?`), dayB.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := s.db.WithContext(ctx).Statement(s.q(`DELETE FROM "requests" WHERE "ts" < ?`), dayB.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	all, err := s.Sums(ctx, dayA, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(all)
	one := got[Dims{Provider: "p1", Model: "m1", ConnectionID: "c-p1", APIKeyID: "k-p1", Endpoint: "/v1/chat/completions"}]
	if one.Requests != 2 || one.Errors != 1 || one.Prompt != 11 || one.Completion != 7 || one.Cost != 2 {
		t.Fatalf("the pruned day was counted wrong: %+v", one)
	}
	if len(got) != 2 {
		t.Fatalf("dims %v", all)
	}

	hours, err := s.Sums(ctx, dayB, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	got = byKey(hours)
	one = got[Dims{Provider: "p1", Model: "m1", ConnectionID: "c-p1", APIKeyID: "k-p1", Endpoint: "/v1/chat/completions"}]
	if one.Requests != 1 || one.Prompt != 1 {
		t.Fatalf("the hourly window counted the pruned day: %+v", one)
	}
	if one.Last != dayB.Add(time.Hour) {
		t.Fatalf("last %v", one.Last)
	}
}

func TestTheWindowStartExcludesWhatCameBefore(t *testing.T) {
	s := newStore(t)
	dayA, dayB, _ := usageRows(t, s)
	sums, err := s.Sums(context.Background(), dayB, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := byKey(sums)
	if len(got) != 2 {
		t.Fatalf("dims %v", sums)
	}
	if got[Dims{Provider: "p1", Model: "m1", ConnectionID: "c-p1", APIKeyID: "k-p1", Endpoint: "/v1/chat/completions"}].Requests != 1 {
		t.Fatalf("the earlier day leaked in: %v", sums)
	}
	if sumsAll, err := s.Sums(context.Background(), dayA, false, nil); err != nil || len(sumsAll) != 2 {
		t.Fatalf("all: %v %v", sumsAll, err)
	}
	if sums, err := s.Sums(context.Background(), time.Time{}, false, nil); err != nil || len(sums) != 2 {
		t.Fatalf("zero from: %v %v", sums, err)
	}
}

func TestBucketsGroupByHourOrByDay(t *testing.T) {
	s := newStore(t)
	dayA, dayB, _ := usageRows(t, s)
	ctx := context.Background()

	// The oldest day comes from the daily rollup, the days after it from the
	// hourly ones, which the caller folds into its own days.
	mixed, err := s.Buckets(ctx, dayA, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) != 3 {
		t.Fatalf("buckets %+v", mixed)
	}
	day := mixed[0]
	if day.Start != dayA || !day.Daily || day.Tokens != 15 || day.Requests != 1 || day.Cost != 1.5 {
		t.Fatalf("the first day %+v", day)
	}
	if mixed[1].Start != dayB.Add(time.Hour) || mixed[1].Daily || mixed[1].Tokens != 3 {
		t.Fatalf("the first hour %+v", mixed[1])
	}
	if mixed[2].Tokens != 7 || mixed[2].Requests != 1 {
		t.Fatalf("the second hour %+v", mixed[2])
	}

	hours, err := s.Buckets(ctx, dayB, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours) != 2 || hours[0].Start != dayB.Add(time.Hour) || hours[1].Tokens != 7 {
		t.Fatalf("hours %+v", hours)
	}
	if hours[0].Daily {
		t.Fatal("an hour bucket was marked daily")
	}
}

func TestAWindowIsLaidOutOverTheTwoRollups(t *testing.T) {
	s := newStore(t)
	dayA, dayB, _ := usageRows(t, s)
	ctx := context.Background()
	from := dayA.Add(5*time.Hour + 30*time.Minute)

	hourly, err := s.spanOf(ctx, from, true)
	if err != nil {
		t.Fatal(err)
	}
	if hourly.split != 0 || hourly.dayFrom != 0 || hourly.hourFrom != hourBucket(from) {
		t.Fatalf("an hourly window reads hours from its own start: %+v", hourly)
	}

	mixed, err := s.spanOf(ctx, from, false)
	if err != nil {
		t.Fatal(err)
	}
	// The first kept hour is on day A, so the hours begin on the next day and
	// the days before it come from the daily rollup.
	if mixed.split != dayB.UnixMilli() || mixed.dayFrom != dayBucket(from) || mixed.hourFrom != mixed.split {
		t.Fatalf("a daily window splits at the first kept day: %+v", mixed)
	}

	all, err := s.spanOf(ctx, time.Time{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if all.dayFrom != 0 || all.hourFrom != all.split {
		t.Fatalf("no start reads everything: %+v", all)
	}
}

func TestLastSeenGivesTheNewestRequestTime(t *testing.T) {
	s := newStore(t)
	_, dayB, _ := usageRows(t, s)
	seen, err := s.LastSeen(context.Background(), dayB, nil)
	if err != nil {
		t.Fatal(err)
	}
	one := seen[Dims{Provider: "p1", Model: "m1", ConnectionID: "c-p1", APIKeyID: "k-p1", Endpoint: "/v1/chat/completions"}]
	if one != dayB.Add(time.Hour) || len(seen) != 2 {
		t.Fatalf("seen %v", seen)
	}
}

func TestRecentIsNewestFirstAndCapped(t *testing.T) {
	s := newStore(t)
	_, dayB, _ := usageRows(t, s)
	rows, err := s.Recent(context.Background(), 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Model != "m2" || rows[1].Status != 500 {
		t.Fatalf("rows %+v", rows)
	}
	if rows[0].Time != dayB.Add(2*time.Hour) {
		t.Fatalf("time %v", rows[0].Time)
	}
}

func TestRecentServedSkipsRowsThatUsedNoTokensAndStaysCapped(t *testing.T) {
	s := newStore(t)
	w := s.NewWriter(WriterOptions{QueueSize: 64, FlushRows: 1000, FlushEvery: time.Hour})
	for i := 0; i < 30; i++ {
		r := rec(i, t0.Add(time.Duration(i)*time.Minute))
		if i%3 == 0 {
			r.PromptTokens, r.CompletionTokens = 0, 0
		}
		w.Enqueue(r)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.RecentServed(context.Background(), 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Fatalf("%d rows, want the 5 newest that used tokens", len(rows))
	}
	for i, r := range rows {
		if r.Prompt+r.Completion == 0 {
			t.Errorf("row %d used no tokens: %+v", i, r)
		}
		if i > 0 && !r.Time.Before(rows[i-1].Time) {
			t.Errorf("row %d is not older than the one before", i)
		}
	}
	if rows[0].Time != t0.Add(29*time.Minute) {
		t.Fatalf("the newest served row is %v", rows[0].Time)
	}
}

func TestProvidersIsDistinctAndSorted(t *testing.T) {
	s := newStore(t)
	usageRows(t, s)
	got, err := s.Providers(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "p1" || got[1] != "p2" {
		t.Fatalf("providers %v", got)
	}
}

func TestFlushedCountsCommittedBatches(t *testing.T) {
	s := newStore(t)
	w := put(t, s, rec(0, t0))
	if w.Flushed() != 1 {
		t.Fatalf("flushed %d", w.Flushed())
	}
	empty := s.NewWriter(WriterOptions{})
	if err := empty.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if empty.Flushed() != 0 {
		t.Fatalf("an empty writer flushed %d", empty.Flushed())
	}
}

// A standard user's answer counts only the keys assigned to them, and a user
// with no keys counts nothing rather than everything.
func TestAKeyScopeNarrowsEveryUsageRead(t *testing.T) {
	s := newStore(t)
	dayA, _, _ := usageRows(t, s)
	ctx := context.Background()

	one, err := s.Sums(ctx, dayA, false, []string{"k-p2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].APIKeyID != "k-p2" || one[0].Requests != 1 {
		t.Fatalf("sums %+v", one)
	}
	if none, err := s.Sums(ctx, dayA, false, []string{}); err != nil || len(none) != 0 {
		t.Fatalf("an empty scope answered %+v %v", none, err)
	}

	rows, err := s.Recent(ctx, 10, []string{"k-p2"})
	if err != nil || len(rows) != 1 || rows[0].Model != "m2" {
		t.Fatalf("recent %+v %v", rows, err)
	}
	served, err := s.RecentServed(ctx, 10, []string{"k-p1"})
	if err != nil || len(served) != 2 {
		t.Fatalf("served %+v %v", served, err)
	}

	seen, err := s.LastSeen(ctx, dayA.Add(-time.Hour), []string{"k-p2"})
	if err != nil || len(seen) != 1 {
		t.Fatalf("last seen %+v %v", seen, err)
	}
	for dims := range seen {
		if dims.APIKeyID != "k-p2" {
			t.Fatalf("last seen named %q", dims.APIKeyID)
		}
	}

	buckets, err := s.Buckets(ctx, dayA, false, []string{"k-p2"})
	if err != nil {
		t.Fatal(err)
	}
	var requests int64
	for _, b := range buckets {
		requests += b.Requests
	}
	if requests != 1 {
		t.Fatalf("buckets counted %d requests, want 1", requests)
	}

	providers, err := s.Providers(ctx, []string{"k-p2"})
	if err != nil || len(providers) != 1 || providers[0] != "p2" {
		t.Fatalf("providers %v %v", providers, err)
	}
	if providers, err := s.Providers(ctx, []string{}); err != nil || len(providers) != 0 {
		t.Fatalf("an empty scope named providers %v %v", providers, err)
	}
}

// PostgreSQL folds an unquoted name to lower case, and the rollup tables are
// created with a capital letter, so a name spliced bare into a query fails there
// and passes on SQLite, which ignores case.
func TestTheRollupTableNamesAreQuoted(t *testing.T) {
	for hourly, want := range map[bool]string{true: `"rollupHourly"`, false: `"rollupDaily"`} {
		if got := quoted(hourly); got != want {
			t.Errorf("quoted(%v) = %s, want %s", hourly, got, want)
		}
	}
}

func TestNoQuerySplicesABareRollupName(t *testing.T) {
	src, err := os.ReadFile("query.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "` + table(") && !strings.HasPrefix(strings.TrimSpace(line), "func quoted") {
			t.Errorf("query.go:%d splices table() into a query; use quoted(): %s", i+1, strings.TrimSpace(line))
		}
	}
}
