package account

import (
	"testing"
	"time"

	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
)

func doc(s string) document.Doc { return document.From(s) }

func stamp(t time.Time) string { return `"` + dialect.FormatTimestamp(t) + `"` }

func TestLockActiveBoundaryAndShapes(t *testing.T) {
	now := t0
	cases := map[string]struct {
		data  string
		model string
		want  bool
	}{
		"future model lock":         {`{"modelLock_m":` + stamp(now.Add(time.Second)) + `}`, "m", true},
		"ends exactly now":          {`{"modelLock_m":` + stamp(now) + `}`, "m", false},
		"past":                      {`{"modelLock_m":` + stamp(now.Add(-time.Second)) + `}`, "m", false},
		"other model":               {`{"modelLock_other":` + stamp(now.Add(time.Hour)) + `}`, "m", false},
		"account lock covers all":   {`{"modelLock___all":` + stamp(now.Add(time.Hour)) + `}`, "m", true},
		"account lock, empty model": {`{"modelLock___all":` + stamp(now.Add(time.Hour)) + `}`, "", true},
		"model lock, empty model":   {`{"modelLock_m":` + stamp(now.Add(time.Hour)) + `}`, "", false},
		"null":                      {`{"modelLock_m":null}`, "m", false},
		"number":                    {`{"modelLock_m":12345}`, "m", false},
		"garbage":                   {`{"modelLock_m":"tomorrow"}`, "m", false},
		"none":                      {`{}`, "m", false},
	}
	for name, c := range cases {
		if got := LockActive(doc(c.data), c.model, now); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestEarliestLockForPicksTheNearestApplicableEnd(t *testing.T) {
	d := doc(`{"modelLock_m":` + stamp(t0.Add(10*time.Minute)) + `,"modelLock___all":` + stamp(t0.Add(3*time.Minute)) + `,"modelLock_x":` + stamp(t0.Add(time.Second)) + `}`)
	got, ok := EarliestLockFor(d, "m", t0)
	if !ok || !got.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("%v %v", got, ok)
	}
	if _, ok := EarliestLockFor(doc(`{"modelLock_m":`+stamp(t0.Add(-time.Minute))+`}`), "m", t0); ok {
		t.Fatal("an ended lock was reported")
	}
}

func TestFormatRetryAfter(t *testing.T) {
	for d, want := range map[time.Duration]string{
		2*time.Minute + 30*time.Second: "reset after 2m 30s",
		time.Second:                    "reset after 1s",
		1500 * time.Millisecond:        "reset after 2s",
		time.Hour:                      "reset after 1h",
		time.Hour + 5*time.Second:      "reset after 1h 5s",
		-time.Second:                   "reset after 0s",
	} {
		if got := FormatRetryAfter(t0.Add(d), t0); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
	if FormatRetryAfter(time.Time{}, t0) != "" {
		t.Fatal("a zero time has text")
	}
}
