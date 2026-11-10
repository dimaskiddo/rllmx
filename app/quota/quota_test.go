package quota

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func spent(until time.Time) Bucket { return Bucket{Remaining: 0, ResetAt: until} }
func room(pct float64) Bucket      { return Bucket{Remaining: pct} }

func TestAModelIsInTheFamilyItDrawsOn(t *testing.T) {
	for model, want := range map[string]string{
		"claude-sonnet-5-5": "claude_gpt", "claude-opus-4-6-thinking": "claude_gpt", "gpt-oss-120b-medium": "claude_gpt",
		"ag/claude-sonnet-5-5-high(high)": "claude_gpt", "gemini-3.1-pro-high": "gemini", "gemini-pro-agent": "gemini",
		"gemini-3.1-flash-image": "", "gemini": "gemini", "claude": "claude_gpt", "llama-3": "", "": "", "CLAUDE-X": "claude_gpt",
	} {
		if got := Family(model); got != want {
			t.Errorf("%q: %q, want %q", model, got, want)
		}
	}
}

func TestTheSpentBucketWithTheLatestResetDecidesAndATightestOtherwise(t *testing.T) {
	soon, late := t0.Add(time.Hour), t0.Add(5*time.Hour)
	for name, tc := range map[string]struct {
		snap   Snapshot
		model  string
		found  bool
		remain float64
		reset  time.Time
	}{
		"nothing known":             {Snapshot{}, "claude-opus-5-5", false, 0, time.Time{}},
		"direct entry":              {Snapshot{"claude-opus-5-5-high": room(40)}, "claude-opus-5-5-high", true, 40, time.Time{}},
		"tier alias":                {Snapshot{"claude-opus-5-5-high": spent(soon)}, "claude-opus-5-5", true, 0, soon},
		"family session spent":      {Snapshot{ClaudeGPTSession: spent(soon), "claude-sonnet-5-5": room(90)}, "claude-sonnet-5-5", true, 0, soon},
		"two spent, latest reset":   {Snapshot{ClaudeGPTSession: spent(soon), ClaudeGPTWeekly: spent(late)}, "claude-sonnet-5-5", true, 0, late},
		"a spent bucket that reset": {Snapshot{ClaudeGPTSession: spent(t0.Add(-time.Minute)), "claude-sonnet-5-5": room(60)}, "claude-sonnet-5-5", true, 60, time.Time{}},
		"only a bucket that reset":  {Snapshot{ClaudeGPTSession: spent(t0.Add(-time.Minute))}, "claude-sonnet-5-5", true, 100, time.Time{}},
		"tightest of the open":      {Snapshot{GeminiSession: room(70), GeminiWeekly: room(20), "gemini-3.1-pro-low": room(55)}, "gemini-3.1-pro-low", true, 20, time.Time{}},
		"gemini ignores claude":     {Snapshot{ClaudeGPTSession: spent(soon)}, "gemini-3.1-pro-low", false, 0, time.Time{}},
		"image uses no family":      {Snapshot{GeminiSession: spent(soon)}, "gemini-3.1-flash-image", false, 0, time.Time{}},
		"spent with no reset time":  {Snapshot{"claude-sonnet-5-5": spent(time.Time{})}, "claude-sonnet-5-5", true, 0, time.Time{}},
	} {
		b, ok := tc.snap.Of(tc.model, t0)
		if ok != tc.found || (ok && (b.Remaining != tc.remain || !b.ResetAt.Equal(tc.reset))) {
			t.Errorf("%s: %+v %v", name, b, ok)
		}
	}
}

func TestAnExhaustedBucketNeedsAFutureReset(t *testing.T) {
	if spent(time.Time{}).Exhausted(t0) || spent(t0.Add(-time.Second)).Exhausted(t0) || room(1).Exhausted(t0) || !spent(t0.Add(time.Second)).Exhausted(t0) {
		t.Fatal("exhaustion is wrong")
	}
}

func TestThePerModelAnswerKeepsOnlyKnownPaidModels(t *testing.T) {
	raw := `{"models":{
		"claude-opus-5-5-high":{"quotaInfo":{"remainingFraction":0.25,"resetTime":"2026-11-01T14:00:00Z"}},
		"gemini-3.1-pro-low":{"quotaInfo":{"remainingFraction":0}},
		"gemini-secret-internal":{"quotaInfo":{"remainingFraction":1}},
		"gemini-3.1-pro-high":{"isInternal":true,"quotaInfo":{"remainingFraction":1}},
		"claude-sonnet-5-5":{},
		"gemini-3.1-flash-image":{"quotaInfo":{"remainingFraction":2,"resetTime":1793000000}}}}`
	s := ParseModels([]byte(raw))
	if len(s) != 3 {
		t.Fatalf("%v", s)
	}
	if b := s["claude-opus-5-5-high"]; b.Remaining != 25 || !b.ResetAt.Equal(time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v", b)
	}
	if s["gemini-3.1-flash-image"].Remaining != 100 || s["gemini-3.1-pro-low"].Remaining != 0 {
		t.Fatalf("%v", s)
	}
	if len(ParseModels([]byte(`[`))) != 0 || len(ParseModels([]byte(`{"models":[]}`))) != 0 {
		t.Fatal("a bad answer produced buckets")
	}
}

func TestTheSummaryAnswerGivesTheFiveHourAndWeeklyBuckets(t *testing.T) {
	raw := `{"quotaSummary":{"groups":[
		{"displayName":"Gemini models","buckets":[{"window":"5h","remainingFraction":0.5,"resetTime":"2026-11-01T17:00:00Z"},{"window":"weekly","remainingFraction":0.9}]},
		{"displayName":"Claude & GPT","buckets":[{"bucketId":"weekly-x","remainingFraction":0.1},{"displayName":"Daily","disabled":true},{"window":"weekly","disabled":true,"remainingFraction":1}]},
		{"displayName":"Other","buckets":[{"window":"5h","remainingFraction":0.3}]}]}}`
	s := ParseSummary([]byte(raw))
	if s[GeminiSession].Remaining != 50 || s[GeminiWeekly].Remaining != 90 || s[ClaudeGPTWeekly].Remaining != 10 {
		t.Fatalf("%v", s)
	}
	if b, ok := s[ClaudeGPTSession]; !ok || b.Remaining != 0 {
		t.Fatalf("a disabled short window must read as spent: %+v %v", b, ok)
	}
	if len(s) != 4 {
		t.Fatalf("%v", s)
	}
	top := ParseSummary([]byte(`{"groups":[{"displayName":"Gemini","buckets":[{"window":"weekly","remainingFraction":0.2}]}]}`))
	if top[GeminiWeekly].Remaining != 20 {
		t.Fatalf("%v", top)
	}
}

func TestResetTimesAreReadInEveryShapeTheProviderUses(t *testing.T) {
	want := time.Unix(1_793_000_000, 0)
	for name, v := range map[string]string{
		"seconds": `1793000000`, "milliseconds": `1793000000000`, "digits": `"1793000000"`, "iso": `"` + want.UTC().Format(time.RFC3339) + `"`,
	} {
		if got := ParseReset(gjson.Parse(v)); !got.Equal(want) {
			t.Errorf("%s: %v", name, got)
		}
	}
	for _, v := range []string{`null`, `""`, `"soon"`, `-5`, `0`, `true`, `{}`} {
		if got := ParseReset(gjson.Parse(v)); !got.IsZero() {
			t.Errorf("%s: %v", v, got)
		}
	}
}

func TestAFamilyWhoseModelsAreAllSpentSpendsItsShortWindow(t *testing.T) {
	later := t0.Add(3 * time.Hour)
	models := Snapshot{"gemini-3.1-pro-low": spent(t0.Add(time.Hour)), "gemini-3.1-pro-high": spent(later), "gemini-3.1-flash-image": room(80)}
	out := Reconcile(models, Snapshot{GeminiSession: {Remaining: 60, ResetAt: t0.Add(10 * time.Hour)}})
	if b := out[GeminiSession]; b.Remaining != 0 || !b.ResetAt.Equal(later) {
		t.Fatalf("%+v", b)
	}
	models["gemini-3.1-pro-low"] = room(5)
	out = Reconcile(models, Snapshot{GeminiSession: {Remaining: 60}})
	if out[GeminiSession].Remaining != 60 {
		t.Fatalf("a family with room was spent: %+v", out)
	}
	if got := Reconcile(Snapshot{}, Snapshot{GeminiSession: room(40)}); got[GeminiSession].Remaining != 40 {
		t.Fatalf("%+v", got)
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newGate(snap Snapshot) (*Gate, *clock, *atomic.Int32) {
	clk := &clock{t: t0}
	var calls atomic.Int32
	g := New(func(context.Context, string, string) Snapshot {
		calls.Add(1)
		if snap == nil {
			return nil
		}
		out := Snapshot{}
		for k, v := range snap {
			out[k] = v
		}
		return out
	})
	g.Now = clk.now
	return g, clk, &calls
}

func TestARefusalOnASpentModelGivesTheExactResetAndTheNextSkipSeesIt(t *testing.T) {
	reset := t0.Add(2 * time.Hour)
	g, _, calls := newGate(Snapshot{"claude-opus-5-5-high": spent(reset)})
	if _, skip := g.Skip("a1", "claude-opus-5-5-high"); skip {
		t.Fatal("skipped before any reading")
	}
	until, ok := g.Refused(context.Background(), "a1", "TOK", "claude-opus-5-5-high")
	if !ok || !until.Equal(reset) {
		t.Fatalf("%v %v", until, ok)
	}
	if u, skip := g.Skip("a1", "claude-opus-5-5-high"); !skip || !u.Equal(reset) {
		t.Fatalf("%v %v", u, skip)
	}
	if _, skip := g.Skip("a2", "claude-opus-5-5-high"); skip {
		t.Fatal("another account was skipped")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d fetches", calls.Load())
	}
}

func TestTheReadingIsFetchedAtMostOncePerThirtySecondsPerAccount(t *testing.T) {
	g, clk, calls := newGate(Snapshot{"claude-opus-5-5-high": room(50)})
	for range 5 {
		g.Refused(context.Background(), "a1", "T", "claude-opus-5-5-high")
	}
	if calls.Load() != 1 {
		t.Fatalf("%d fetches inside the window", calls.Load())
	}
	clk.add(MinRefresh + time.Second)
	g.Refused(context.Background(), "a1", "T", "claude-opus-5-5-high")
	if calls.Load() != 2 {
		t.Fatalf("%d fetches after the window", calls.Load())
	}
	g.Refused(context.Background(), "a2", "T", "claude-opus-5-5-high")
	if calls.Load() != 3 {
		t.Fatalf("another account shared the throttle: %d", calls.Load())
	}
}

func TestConcurrentRefusalsShareOneFetch(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	g := New(func(context.Context, string, string) Snapshot {
		calls.Add(1)
		<-release
		return Snapshot{"claude-opus-5-5-high": spent(t0.Add(time.Hour))}
	})
	g.Now = func() time.Time { return t0 }
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := g.Refused(context.Background(), "a1", "T", "claude-opus-5-5-high"); !ok {
				t.Error("a waiter did not see the reading")
			}
		}()
	}
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d fetches", calls.Load())
	}
}

func TestThreeRefusalsInAMinuteBlockAModelThatStillShowsRoom(t *testing.T) {
	g, clk, _ := newGate(Snapshot{"claude-opus-5-5-high": room(80)})
	ctx := context.Background()
	for i := range 2 {
		if _, ok := g.Refused(ctx, "a1", "T", "claude-opus-5-5-high"); ok {
			t.Fatalf("blocked on refusal %d", i+1)
		}
		clk.add(5 * time.Second)
	}
	until, ok := g.Refused(ctx, "a1", "T", "claude-opus-5-5-high")
	if !ok || !until.Equal(clk.now().Add(StrikeBlock)) {
		t.Fatalf("%v %v", until, ok)
	}
	if u, skip := g.Skip("a1", "claude-opus-5-5-high"); !skip || !u.Equal(until) {
		t.Fatalf("%v %v", u, skip)
	}
	clk.add(StrikeBlock + time.Second)
	if _, skip := g.Skip("a1", "claude-opus-5-5-high"); skip {
		t.Fatal("the block outlived its time")
	}
}

func TestStrikesCountInsideOneWindowAndASuccessClearsThem(t *testing.T) {
	g, clk, _ := newGate(nil)
	ctx := context.Background()
	g.Refused(ctx, "a1", "T", "m")
	g.Refused(ctx, "a1", "T", "m")
	clk.add(StrikeWindow + time.Second)
	if _, ok := g.Refused(ctx, "a1", "T", "m"); ok {
		t.Fatal("strikes from two windows were added")
	}
	g.Refused(ctx, "a1", "T", "m")
	g.Succeeded("a1", "m")
	if _, ok := g.Refused(ctx, "a1", "T", "m"); ok {
		t.Fatal("a success did not clear the strikes")
	}
}

func TestABlockSurvivesAFreshOptimisticReading(t *testing.T) {
	g, clk, _ := newGate(Snapshot{"claude-opus-5-5-high": room(100)})
	ctx := context.Background()
	for range StrikeThreshold {
		g.Refused(ctx, "a1", "T", "claude-opus-5-5-high")
		clk.add(time.Second)
	}
	clk.add(MinRefresh)
	g.Refused(ctx, "a1", "T", "claude-opus-5-5-high")
	if _, skip := g.Skip("a1", "claude-opus-5-5-high"); !skip {
		t.Fatal("a fresh reading lifted a block made on purpose")
	}
}

func TestAFailedFetchLeavesTheOldReading(t *testing.T) {
	g, clk, _ := newGate(Snapshot{"claude-opus-5-5-high": spent(t0.Add(time.Hour))})
	g.Refused(context.Background(), "a1", "T", "claude-opus-5-5-high")
	g.Fetch = func(context.Context, string, string) Snapshot { return nil }
	clk.add(MinRefresh + time.Second)
	g.Refused(context.Background(), "a1", "T", "claude-opus-5-5-high")
	if _, skip := g.Skip("a1", "claude-opus-5-5-high"); !skip {
		t.Fatal("a failed fetch erased the reading")
	}
}

func TestForgetDropsEverythingForAnAccountAndTheTablesAreBounded(t *testing.T) {
	g, _, _ := newGate(Snapshot{"claude-opus-5-5-high": spent(t0.Add(time.Hour))})
	g.Refused(context.Background(), "a1", "T", "claude-opus-5-5-high")
	g.Forget("a1")
	if _, skip := g.Skip("a1", "claude-opus-5-5-high"); skip {
		t.Fatal("a forgotten account is still skipped")
	}
	g.Fetch = nil
	for i := range maxAccounts + 50 {
		g.Refused(context.Background(), string(rune('a'+i%26))+time.Duration(i).String(), "T", "m")
	}
	if len(g.last) > maxAccounts+1 || len(g.strikes) > maxAccounts+1 {
		t.Fatalf("%d, %d", len(g.last), len(g.strikes))
	}
}

func FuzzQuotaParse(f *testing.F) {
	for _, s := range []string{`{}`, `[`, `{"models":{"gemini-3.1-pro-low":{"quotaInfo":{"remainingFraction":"x"}}}}`, `{"groups":[{"buckets":[{"window":"5h"}]}]}`, `{"groups":7}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		for _, s := range []Snapshot{ParseModels(raw), ParseSummary(raw)} {
			if len(s) > maxBuckets*2 {
				t.Fatalf("%d buckets", len(s))
			}
			for k, b := range s {
				if b.Remaining < 0 || b.Remaining > 100 {
					t.Fatalf("%s: %v", k, b.Remaining)
				}
			}
			s.Of("claude-opus-5-5", t0)
		}
	})
}
