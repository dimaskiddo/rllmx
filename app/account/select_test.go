package account

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"rllmx/app/dialect"
	"rllmx/app/document"
	"rllmx/app/repository"
	"rllmx/tests"
	"rllmx/tests/store"
)

type rig struct {
	st   *store.Store
	sel  *Selector
	rep  *Reporter
	log  *tests.Log
	now  time.Time
	mu   sync.Mutex
	conf string
}

func (r *rig) clock() time.Time { r.mu.Lock(); defer r.mu.Unlock(); return r.now }

func (r *rig) advance(d time.Duration) { r.mu.Lock(); r.now = r.now.Add(d); r.mu.Unlock() }

type settingsFn func(context.Context) (document.Doc, error)

func (f settingsFn) Effective(ctx context.Context) (document.Doc, error) { return f(ctx) }

func newRig(t *testing.T, settings string) *rig {
	t.Helper()
	st := store.Migrated(t)
	r := &rig{st: st, log: &tests.Log{}, now: time.Now().UTC().Truncate(time.Millisecond), conf: settings}
	src := settingsFn(func(context.Context) (document.Doc, error) { return document.From(r.conf), nil })
	r.sel = NewSelector(SelectorOptions{Conns: st.Connections(), Settings: src, Log: r.log, Now: r.clock})
	r.rep = NewReporter(st.Connections(), r.log, r.clock)
	return r
}

func (r *rig) add(t *testing.T, provider, name string, fields map[string]string) repository.Connection {
	t.Helper()
	if fields == nil {
		fields = map[string]string{"apiKey": `"k"`}
	}
	psd := fields["providerSpecificData"]
	delete(fields, "providerSpecificData")
	c, err := r.st.Connections().Create(context.Background(), repository.ConnInput{Provider: provider, AuthType: "apikey", Name: name, Fields: fields, PSD: psd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (r *rig) get(t *testing.T, id string) repository.Connection {
	t.Helper()
	c, ok, err := r.st.Connections().Get(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("get %s: %v %v", id, ok, err)
	}
	return c
}

func (r *rig) pick(t *testing.T, req Request) repository.Connection {
	t.Helper()
	sel, err := r.sel.Select(context.Background(), req)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	return sel.Conn
}

func (r *rig) fail(t *testing.T, id string, status int, text string) Outcome {
	t.Helper()
	out, err := r.rep.Fail(context.Background(), FailInput{ConnID: id, Provider: "p", Model: "m", Status: status, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFillFirstUsesPriorityOrderAndWritesNothing(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	r.add(t, "p", "b", nil)
	before := r.get(t, a.ID).Data
	for range 3 {
		if got := r.pick(t, Request{Provider: "p", Model: "m"}); got.ID != a.ID {
			t.Fatalf("picked %s", got.Name)
		}
	}
	if r.get(t, a.ID).Data != before {
		t.Fatal("fill-first wrote the row")
	}
}

func TestOtherProvidersAndInactiveRowsAreNotCandidates(t *testing.T) {
	r := newRig(t, `{}`)
	inactive := false
	off := r.add(t, "p", "off", nil)
	r.add(t, "q", "other", nil)
	on := r.add(t, "p", "on", nil)
	if _, err := r.st.Connections().Update(context.Background(), off.ID, repository.ConnPatch{IsActive: &inactive}); err != nil {
		t.Fatal(err)
	}
	if got := r.pick(t, Request{Provider: "p"}); got.ID != on.ID {
		t.Fatalf("picked %s", got.Name)
	}
	if _, err := r.sel.Select(context.Background(), Request{Provider: "nobody"}); !errors.Is(err, ErrNoConnections) {
		t.Fatalf("err %v", err)
	}
}

func TestExcludeSkipsTriedConnections(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	b := r.add(t, "p", "b", nil)
	if got := r.pick(t, Request{Provider: "p", Exclude: map[string]bool{a.ID: true}}); got.ID != b.ID {
		t.Fatalf("picked %s", got.Name)
	}
	_, err := r.sel.Select(context.Background(), Request{Provider: "p", Exclude: map[string]bool{a.ID: true, b.ID: true}})
	if !errors.Is(err, ErrNoConnections) {
		t.Fatalf("err %v", err)
	}
}

func TestAnExpiredConnectionIsNeverSelected(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "dead", map[string]string{"apiKey": `"k"`, "testStatus": `"expired"`})
	b := r.add(t, "p", "live", nil)
	if got := r.pick(t, Request{Provider: "p"}); got.ID != b.ID {
		t.Fatalf("picked %s", got.Name)
	}
	_, err := r.sel.Select(context.Background(), Request{Provider: "p", Exclude: map[string]bool{b.ID: true}})
	if !errors.Is(err, ErrNoConnections) {
		t.Fatalf("an expired connection was the last resort: %v", err)
	}
	_ = a
}

func TestCodexEnabledModelsListFiltersAccounts(t *testing.T) {
	r := newRig(t, `{}`)
	psd := func(models string) map[string]string {
		return map[string]string{"apiKey": `"k"`, "providerSpecificData": `{"enabledModels":` + models + `}`}
	}
	only := r.add(t, "codex", "only-a", psd(`["gpt-a"]`))
	any := r.add(t, "codex", "any", psd(`[]`))
	none := r.add(t, "codex", "unset", nil)
	if got := r.pick(t, Request{Provider: "codex", RequestedModel: "gpt-a"}); got.ID != only.ID {
		t.Errorf("listed model: %s", got.Name)
	}
	if got := r.pick(t, Request{Provider: "codex", RequestedModel: "gpt-b"}); got.ID != any.ID {
		t.Errorf("unlisted model: %s", got.Name)
	}
	if got := r.pick(t, Request{Provider: "codex", RequestedModel: "gpt-b", Exclude: map[string]bool{any.ID: true}}); got.ID != none.ID {
		t.Errorf("unset list: %s", got.Name)
	}
	// the list is Codex-only
	r.add(t, "other", "o", psd(`["x"]`))
	if got := r.pick(t, Request{Provider: "other", RequestedModel: "y"}); got.Name != "o" {
		t.Errorf("another provider was filtered: %s", got.Name)
	}
}

func TestPreferPinsAnAvailableConnectionOnly(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	b := r.add(t, "p", "b", nil)
	before := r.get(t, b.ID).Data
	if got := r.pick(t, Request{Provider: "p", Model: "m", Prefer: b.ID}); got.ID != b.ID {
		t.Fatalf("the pin was ignored: %s", got.Name)
	}
	if r.get(t, b.ID).Data != before {
		t.Fatal("a pinned pick wrote the row")
	}
	r.fail(t, b.ID, 429, "")
	if got := r.pick(t, Request{Provider: "p", Model: "m", Prefer: b.ID}); got.ID != a.ID {
		t.Fatalf("a locked pin won: %s", got.Name)
	}
	if got := r.pick(t, Request{Provider: "p", Model: "m", Prefer: "no-such-id"}); got.ID != a.ID {
		t.Fatalf("an unknown pin: %s", got.Name)
	}
	if got := r.pick(t, Request{Provider: "p", Model: "other-model", Prefer: b.ID}); got.ID != b.ID {
		t.Fatalf("a lock on another model blocked the pin: %s", got.Name)
	}
}

func TestRoundRobinStaysThenRotates(t *testing.T) {
	r := newRig(t, `{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":2}`)
	a := r.add(t, "p", "a", nil)
	b := r.add(t, "p", "b", nil)
	c := r.add(t, "p", "c", nil)
	var got []string
	for range 7 {
		got = append(got, r.pick(t, Request{Provider: "p", Model: "m"}).Name)
		r.advance(time.Millisecond)
	}
	want := []string{"a", "a", "b", "b", "c", "c", "a"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%v, want %v", got, want)
	}
	if n, _ := r.get(t, c.ID).Doc().GetInt("consecutiveUseCount"); n != 2 {
		t.Errorf("count persisted as %d", n)
	}
	_, _ = a, b
}

func TestRoundRobinSurvivesAFreshSelector(t *testing.T) {
	r := newRig(t, `{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":3}`)
	r.add(t, "p", "a", nil)
	r.add(t, "p", "b", nil)
	if got := r.pick(t, Request{Provider: "p"}).Name; got != "a" {
		t.Fatal(got)
	}
	r.advance(time.Millisecond)
	fresh := NewSelector(SelectorOptions{Conns: r.st.Connections(), Settings: settingsFn(func(context.Context) (document.Doc, error) { return document.From(r.conf), nil }), Now: r.clock})
	sel, err := fresh.Select(context.Background(), Request{Provider: "p"})
	if err != nil || sel.Conn.Name != "a" {
		t.Fatalf("a restart lost the rotation state: %v %v", sel.Conn.Name, err)
	}
}

func TestRoundRobinNeverUsedGoesFirstAndTiesFollowPriority(t *testing.T) {
	r := newRig(t, `{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":1}`)
	r.add(t, "p", "a", nil)
	r.add(t, "p", "b", nil)
	r.add(t, "p", "c", nil)
	if got := r.pick(t, Request{Provider: "p"}).Name; got != "a" {
		t.Fatalf("first pick %s", got)
	}
	r.advance(time.Millisecond)
	if got := r.pick(t, Request{Provider: "p"}).Name; got != "b" {
		t.Fatalf("a never-used connection did not go next: %s", got)
	}
}

func TestTwoUsesInOneMillisecondStillOrder(t *testing.T) {
	r := newRig(t, `{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":1}`)
	r.add(t, "p", "a", nil)
	r.add(t, "p", "b", nil)
	var got []string
	for range 4 { // the clock never moves
		got = append(got, r.pick(t, Request{Provider: "p"}).Name)
	}
	if fmt.Sprint(got) != "[a b a b]" {
		t.Fatalf("%v: uses in the same millisecond did not order", got)
	}
}

func TestStickyLimitReading(t *testing.T) {
	cases := map[string]struct {
		settings string
		want     int
	}{
		"default":                       {`{}`, 3},
		"global number":                 {`{"stickyRoundRobinLimit":5}`, 5},
		"global string":                 {`{"stickyRoundRobinLimit":"4"}`, 4},
		"zero falls through":            {`{"stickyRoundRobinLimit":0}`, 3},
		"negative falls through":        {`{"stickyRoundRobinLimit":-2}`, 3},
		"unparsable falls through":      {`{"stickyRoundRobinLimit":"many"}`, 3},
		"provider wins":                 {`{"stickyRoundRobinLimit":5,"providerStrategies":{"p":{"stickyRoundRobinLimit":2}}}`, 2},
		"provider zero falls to global": {`{"stickyRoundRobinLimit":5,"providerStrategies":{"p":{"stickyRoundRobinLimit":0}}}`, 5},
		"dotted provider id":            {`{"providerStrategies":{"a.b":{"stickyRoundRobinLimit":7}}}`, 3},
	}
	for name, c := range cases {
		prov := "p"
		if name == "dotted provider id" {
			prov = "a.b"
			c.want = 7
		}
		if got := stickyLimit(document.From(c.settings), prov); got != c.want {
			t.Errorf("%s: %d, want %d", name, got, c.want)
		}
	}
}

func TestStrategyReading(t *testing.T) {
	for settings, want := range map[string]string{
		`{}`: "fill-first", `{"fallbackStrategy":"round-robin"}`: "round-robin", `{"fallbackStrategy":"surprise"}`: "fill-first",
		`{"fallbackStrategy":"round-robin","providerStrategies":{"p":{"fallbackStrategy":"fill-first"}}}`: "fill-first",
		`{"providerStrategies":{"p":{"fallbackStrategy":"round-robin"}}}`:                                 "round-robin",
		`{"providerStrategies":{"p":{"fallbackStrategy":"nonsense"}},"fallbackStrategy":"round-robin"}`:   "fill-first",
	} {
		if got := strategyFor(document.From(settings), "p"); got != want {
			t.Errorf("%s: %s, want %s", settings, got, want)
		}
	}
}

func TestAFailedSettingsReadIsAnErrorAndIsNotCached(t *testing.T) {
	r := newRig(t, `{}`)
	r.add(t, "p", "a", nil)
	fail := true
	r.sel = NewSelector(SelectorOptions{Conns: r.st.Connections(), Now: r.clock, Settings: settingsFn(func(context.Context) (document.Doc, error) {
		if fail {
			return document.Doc{}, errors.New("disk")
		}
		return document.From(`{}`), nil
	})})
	if _, err := r.sel.Select(context.Background(), Request{Provider: "p"}); err == nil {
		t.Fatal("a failed read became a guess")
	}
	fail = false
	if _, err := r.sel.Select(context.Background(), Request{Provider: "p"}); err != nil {
		t.Fatalf("the failure was cached: %v", err)
	}
}

func TestTheSettingsSnapshotRefreshesAtTheTTLAndOnInvalidate(t *testing.T) {
	r := newRig(t, `{}`)
	reads := 0
	cache := &settingsCache{now: r.clock, src: settingsFn(func(context.Context) (document.Doc, error) { reads++; return document.From(`{}`), nil })}
	ctx := context.Background()
	for range 3 {
		_, _ = cache.get(ctx)
	}
	r.advance(SettingsTTL - time.Millisecond)
	_, _ = cache.get(ctx)
	if reads != 1 {
		t.Fatalf("%d reads before the TTL", reads)
	}
	r.advance(time.Millisecond)
	_, _ = cache.get(ctx)
	if reads != 2 {
		t.Fatalf("%d reads at the TTL", reads)
	}
	cache.drop()
	_, _ = cache.get(ctx)
	if reads != 3 {
		t.Fatalf("%d reads after drop", reads)
	}
}

func TestAllLockedGivesTheEarliestEndAndTheLeadError(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	b := r.add(t, "p", "b", nil)
	r.fail(t, a.ID, 401, "bad key a")
	r.fail(t, b.ID, 429, "slow down b")
	_, err := r.sel.Select(context.Background(), Request{Provider: "p", Model: "m"})
	var locked *AllLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("err %v", err)
	}
	if want := r.now.Add(2 * time.Second); !locked.RetryAt.Equal(want) {
		t.Errorf("RetryAt %v, want %v", locked.RetryAt, want)
	}
	if locked.LastError != "slow down b" || locked.LastErrorCode != 429 || locked.Human != "reset after 2s" {
		t.Errorf("%+v", locked)
	}
	r.advance(3 * time.Second)
	if got := r.pick(t, Request{Provider: "p", Model: "m"}); got.ID != b.ID {
		t.Fatalf("b should be free again, got %s", got.Name)
	}
}

func TestALockOnAnotherModelDoesNotBlock(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	r.fail(t, a.ID, 429, "")
	if got := r.pick(t, Request{Provider: "p", Model: "other"}); got.ID != a.ID {
		t.Fatal("a lock on model m blocked model other")
	}
}

func TestACorruptRowIsSkippedAndNeverWritten(t *testing.T) {
	r := newRig(t, `{"fallbackStrategy":"round-robin"}`)
	bad := r.add(t, "p", "bad", nil)
	good := r.add(t, "p", "good", nil)
	if err := r.st.SQ.DB.Statement(`UPDATE "providerConnections" SET "data"='{"a":1,"a":2}' WHERE "id"=?`, bad.ID); err != nil {
		t.Fatal(err)
	}
	sel, err := r.sel.Select(context.Background(), Request{Provider: "p"})
	if err != nil || sel.Conn.ID != good.ID || sel.Skipped != 1 {
		t.Fatalf("%v %+v", err, sel)
	}
	if r.get(t, bad.ID).Data != `{"a":1,"a":2}` {
		t.Fatal("the corrupt row was rewritten")
	}
	_, err = r.sel.Select(context.Background(), Request{Provider: "p", Exclude: map[string]bool{good.ID: true}})
	if !errors.Is(err, ErrNoConnections) {
		t.Fatalf("a corrupt row was the last resort: %v", err)
	}
}

func TestACancelledContextFailsBeforeAnyRead(t *testing.T) {
	r := newRig(t, `{}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.sel.Select(ctx, Request{Provider: "p"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
}

func TestSelectionIsSerializedPerProvider(t *testing.T) {
	r := newRig(t, `{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":1000}`)
	r.add(t, "p", "a", nil)
	r.add(t, "p", "b", nil)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.sel.Select(context.Background(), Request{Provider: "p"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var total int64
	for _, c := range func() []repository.Connection {
		l, _ := r.st.Connections().List(context.Background(), repository.ConnFilter{Provider: "p"})
		return l
	}() {
		n, _ := c.Doc().GetInt("consecutiveUseCount")
		total += n
	}
	if total != 20 {
		t.Fatalf("counters add up to %d, want 20: concurrent picks raced", total)
	}
}

func TestADeclinedLockWritesNothingForARequestScopedError(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	before := r.get(t, a.ID)
	out := r.fail(t, a.ID, 400, "context too long")
	if out.Fallback || !out.RequestScoped {
		t.Fatalf("%+v", out)
	}
	after := r.get(t, a.ID)
	if before.Data != after.Data || before.UpdatedAt != after.UpdatedAt {
		t.Fatal("a request-scoped error wrote the row")
	}
}

func TestFailGrowsTheBackoffAndSucceedResetsIt(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	for i, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		r.fail(t, a.ID, 429, "")
		d := r.get(t, a.ID).Doc()
		until, _ := lockUntil(d, repository.LockKey("m"))
		if got := until.Sub(r.now); got != want {
			t.Errorf("failure %d: cooldown %v, want %v", i+1, got, want)
		}
		if lvl, _ := d.GetInt("backoffLevel"); int(lvl) != i+1 {
			t.Errorf("level %d", lvl)
		}
	}
	// the repository stamps failures with the wall clock, so "the request began
	// after the failure" is a real time after it
	started := time.Now().Add(time.Minute)
	if err := r.rep.Succeed(context.Background(), a.ID, "m", started); err != nil {
		t.Fatal(err)
	}
	d := r.get(t, a.ID).Doc()
	if lvl, _ := d.GetInt("backoffLevel"); lvl != 0 {
		t.Errorf("level after success %d", lvl)
	}
	if st, _ := d.GetString("testStatus"); st != "active" {
		t.Errorf("status %q", st)
	}
	if LockActive(d, "m", r.now) {
		t.Error("the lock survived a success")
	}
}

func TestAFixedCooldownBetweenTwo429sKeepsTheLevel(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	r.fail(t, a.ID, 429, "")
	r.fail(t, a.ID, 401, "")
	r.fail(t, a.ID, 429, "")
	if lvl, _ := r.get(t, a.ID).Doc().GetInt("backoffLevel"); lvl != 2 {
		t.Fatalf("level %d: the 401 reset the backoff", lvl)
	}
}

func TestConcurrentFailuresAllCount(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.rep.Fail(context.Background(), FailInput{ConnID: a.ID, Provider: "p", Model: "m", Status: 429}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if lvl, _ := r.get(t, a.ID).Doc().GetInt("backoffLevel"); lvl != 12 {
		t.Fatalf("level %d, want 12: the level was read outside the write", lvl)
	}
}

func TestTheErrorTextIsStoredButNeverLogged(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	secret := "sk-live-prompt-fragment-9917"
	r.fail(t, a.ID, 500, "upstream said "+secret)
	if last, _ := r.get(t, a.ID).Doc().GetString("lastError"); last == "" {
		t.Fatal("the dashboard has no error to show")
	}
	for _, line := range r.log.Lines() {
		if contains(line, secret) {
			t.Fatalf("the error text reached the log: %q", line)
		}
	}
	if len(r.log.Lines()) == 0 {
		t.Fatal("nothing was logged about the lock")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestAccountWideLockBlocksEveryModel(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "github", "gh", nil)
	r.rep = NewReporter(r.st.Connections(), r.log, func() time.Time { return time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC) })
	r.now = time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)
	if _, err := r.rep.Fail(context.Background(), FailInput{ConnID: a.ID, Provider: "github", Model: "gpt-5", Status: 402, Text: "You've reached your additional usage limit for your plan"}); err != nil {
		t.Fatal(err)
	}
	d := r.get(t, a.ID).Doc()
	if !LockActive(d, "some-other-model", r.now) {
		t.Fatalf("the account lock does not cover other models: %s", d.String())
	}
	if _, ok := d.GetString(repository.LockKey("gpt-5")); ok {
		t.Fatal("a per-model lock was written for an account-wide error")
	}
}

func TestSucceedKeepsAFailureNewerThanTheRequest(t *testing.T) {
	r := newRig(t, `{}`)
	a := r.add(t, "p", "a", nil)
	started := time.Now().Add(-time.Minute)
	r.fail(t, a.ID, 500, "boom")
	if err := r.rep.Succeed(context.Background(), a.ID, "m", started); err != nil {
		t.Fatal(err)
	}
	if st, _ := r.get(t, a.ID).Doc().GetString("testStatus"); st != "unavailable" {
		t.Fatalf("an older success undid a newer failure: %q", st)
	}
}

func TestEmptyConnectionIDsAreIgnored(t *testing.T) {
	r := newRig(t, `{}`)
	if out, err := r.rep.Fail(context.Background(), FailInput{Status: 500}); err != nil || out.Fallback {
		t.Fatalf("%+v %v", out, err)
	}
	if err := r.rep.Succeed(context.Background(), "", "m", time.Time{}); err != nil {
		t.Fatal(err)
	}
}

var _ = dialect.Now
