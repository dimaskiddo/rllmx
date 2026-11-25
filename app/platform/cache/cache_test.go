package cache

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rllmx/tests"
)

func newCache(t *testing.T) (*Cache, *tests.Cache) {
	t.Helper()
	fake := tests.NewCache()
	return New(fake), fake
}

func TestCredentialGroupsNeverReachTheSharedStore(t *testing.T) {
	c, fake := newCache(t)

	for _, g := range []Group{Connections, Refresh, OAuth, Overrides, Group("brand-new-group")} {
		c.Set(g, "k", []byte("secret"), time.Minute)
		if got, ok := c.Get(g, "k"); !ok || string(got) != "secret" {
			t.Errorf("%s: the value did not come back: %q %v", g, got, ok)
		}
		if keys := fake.StoreNamed("redis").Keys(""); len(keys) != 0 {
			t.Errorf("%s reached the shared store: %v", g, keys)
		}
	}
	if len(fake.StoreNamed("memory").Keys("")) == 0 {
		t.Fatal("nothing was stored in memory")
	}
}

func TestSharedGroupsUseTheSharedStore(t *testing.T) {
	c, fake := newCache(t)

	for _, g := range []Group{Settings, APIKeys, Models, Limiter, Response, ToolCalls} {
		c.Set(g, "k", []byte("v"), time.Minute)
		if got, ok := c.Get(g, "k"); !ok || string(got) != "v" {
			t.Errorf("%s: %q %v", g, got, ok)
		}
	}
	if got := len(fake.StoreNamed("redis").Keys("")); got < 6 {
		t.Fatalf("the shared store holds %d keys, want one per shared group", got)
	}
}

func TestWithoutRedisTheSharedStoreIsMemory(t *testing.T) {
	fake := tests.NewCache()
	c := New(fake, WithoutShared())
	c.Set(Settings, "k", []byte("v"), time.Minute)
	if len(fake.StoreNamed("redis").Keys("")) != 0 {
		t.Fatal("a shared group reached Redis although none is configured")
	}
	if got, ok := c.Get(Settings, "k"); !ok || string(got) != "v" {
		t.Fatalf("%q %v", got, ok)
	}
}

func TestKeysAreGroupGenerationKey(t *testing.T) {
	c, fake := newCache(t)
	c.Set(Settings, "pipeline", []byte("v"), time.Minute)
	keys := fake.StoreNamed("redis").Keys("settings:")
	if len(keys) != 1 || keys[0] != "settings:0:pipeline" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestStoredValueIsACopy(t *testing.T) {
	c, _ := newCache(t)

	in := []byte("abc")
	c.Set(Settings, "k", in, time.Minute)
	in[0] = 'X'
	got, _ := c.Get(Settings, "k")
	if string(got) != "abc" {
		t.Fatalf("the stored value followed the caller's slice: %q", got)
	}
	got[0] = 'Y'
	again, _ := c.Get(Settings, "k")
	if string(again) != "abc" {
		t.Fatalf("a caller changed the cached value: %q", again)
	}
}

func TestSetWithoutAPositiveLifetimeStoresNothingAndRemovesTheOldValue(t *testing.T) {
	c, _ := newCache(t)

	c.Set(Settings, "k", []byte("v"), time.Minute)
	for _, ttl := range []time.Duration{0, -time.Second} {
		c.Set(Settings, "k", []byte("new"), ttl)
		if _, ok := c.Get(Settings, "k"); ok {
			t.Errorf("ttl %v left a value behind", ttl)
		}
		c.Set(Settings, "k", []byte("v"), time.Minute)
	}
}

func TestOversizedKeysAndValuesStoreNothing(t *testing.T) {
	c, fake := newCache(t)

	c.Set(Settings, "big", make([]byte, MaxValueBytes+1), time.Minute)
	c.Set(Settings, strings.Repeat("k", MaxKeyBytes+1), []byte("v"), time.Minute)
	if keys := fake.StoreNamed("redis").Keys(""); len(keys) != 0 {
		t.Fatalf("stored %v", keys)
	}
	c.Set(Settings, "ok", make([]byte, MaxValueBytes), time.Minute)
	if _, ok := c.Get(Settings, "ok"); !ok {
		t.Fatal("a value at the limit was refused")
	}
}

func TestDeleteRemovesAValue(t *testing.T) {
	c, _ := newCache(t)

	c.Set(Settings, "k", []byte("v"), time.Minute)
	c.Delete(Settings, "k")
	if _, ok := c.Get(Settings, "k"); ok {
		t.Fatal("still there")
	}
}

func TestPurgeInvalidatesAGroupAndOnlyThatGroup(t *testing.T) {
	c, _ := newCache(t)

	c.Set(Settings, "a", []byte("1"), time.Minute)
	c.Set(Models, "a", []byte("2"), time.Minute)
	c.Purge(Settings)
	if _, ok := c.Get(Settings, "a"); ok {
		t.Fatal("a purged value was served")
	}
	if got, ok := c.Get(Models, "a"); !ok || string(got) != "2" {
		t.Fatalf("another group lost its value: %q %v", got, ok)
	}
	c.Set(Settings, "a", []byte("3"), time.Minute)
	if got, ok := c.Get(Settings, "a"); !ok || string(got) != "3" {
		t.Fatalf("a value stored after the purge: %q %v", got, ok)
	}
}

func TestPurgeSeenByAnotherInstanceSharingTheStore(t *testing.T) {
	fake := tests.NewCache()
	a, b := New(fake), New(fake)

	a.Set(Settings, "k", []byte("v"), time.Minute)
	b.Purge(Settings)
	if _, ok := a.Get(Settings, "k"); ok {
		t.Fatal("a purge by one instance was not seen by the other: the generation lives in the store")
	}
}

func TestPurgeOfAMemoryOnlyGroupStaysInMemory(t *testing.T) {
	c, fake := newCache(t)
	c.Purge(Connections)
	if len(fake.StoreNamed("redis").Keys("")) != 0 {
		t.Fatal("a credential group's generation counter was written to the shared store")
	}
}

func TestFailOpen(t *testing.T) {
	c, fake := newCache(t)

	c.Set(Settings, "k", []byte("v"), time.Minute)
	fake.StoreNamed("redis").SetDown(true)

	if _, ok := c.Get(Settings, "k"); ok {
		t.Error("a down store must be a miss")
	}
	c.Set(Settings, "k2", []byte("v"), time.Minute)
	c.Delete(Settings, "k")
	c.Purge(Settings)

	fake.StoreNamed("redis").SetDown(false)
	if _, ok := c.Get(Settings, "k2"); ok {
		t.Error("a write that failed while the store was down appeared later")
	}
}

func TestGenerationReadFailureIsAMiss(t *testing.T) {
	c, fake := newCache(t)

	c.Set(Settings, "k", []byte("v"), time.Minute)
	fake.StoreNamed("redis").SetDown(true)
	for range 3 {
		if _, ok := c.Get(Settings, "k"); ok {
			t.Fatal("served a value with no generation to check it against")
		}
	}
}

func TestAMissingStoreIsAMissNeverAPanic(t *testing.T) {
	l := &tests.Log{}
	c := New(noStores{}, WithLog(l))

	c.Set(Settings, "k", []byte("v"), time.Minute)
	if _, ok := c.Get(Settings, "k"); ok {
		t.Fatal("value from nowhere")
	}
	c.Delete(Settings, "k")
	c.Purge(Settings)
	if lines := l.Lines(); len(lines) != 1 || !strings.Contains(lines[0], "not available") {
		t.Fatalf("a store the facade could not build must be reported once: %q", lines)
	}
}

func TestLoadRunsOnceForManyCallers(t *testing.T) {
	c, _ := newCache(t)
	var calls atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]string, 20)
	for i := range results {
		wg.Go(func() {
			v, err := c.Load(context.Background(), Models, "state", time.Minute, func(context.Context) ([]byte, error) {
				calls.Add(1)
				<-release
				return []byte("loaded"), nil
			})
			if err != nil {
				t.Error(err)
			}
			results[i] = string(v)
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("the loader ran %d times", calls.Load())
	}
	for _, r := range results {
		if r != "loaded" {
			t.Fatalf("results %q", results)
		}
	}
}

func TestLoadServesFromTheCacheAfterTheFirstLoad(t *testing.T) {
	c, _ := newCache(t)
	var calls int
	load := func(context.Context) ([]byte, error) { calls++; return []byte("v"), nil }
	for range 3 {
		if v, err := c.Load(context.Background(), Models, "k", time.Minute, load); err != nil || string(v) != "v" {
			t.Fatalf("%q %v", v, err)
		}
	}
	if calls != 1 {
		t.Fatalf("loaded %d times", calls)
	}
}

func TestLoadDoesNotCacheAnError(t *testing.T) {
	c, _ := newCache(t)
	boom := errors.New("boom")
	if _, err := c.Load(context.Background(), Models, "k", time.Minute, func(context.Context) ([]byte, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	v, err := c.Load(context.Background(), Models, "k", time.Minute, func(context.Context) ([]byte, error) { return []byte("ok"), nil })
	if err != nil || string(v) != "ok" {
		t.Fatalf("%q %v", v, err)
	}
}

func TestLoadTurnsAPanicIntoAnError(t *testing.T) {
	c, _ := newCache(t)
	_, err := c.Load(context.Background(), Models, "k", time.Minute, func(context.Context) ([]byte, error) { panic("bad") })
	if !errors.Is(err, ErrLoadPanic) {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadCallerCancelDoesNotFailTheOthers(t *testing.T) {
	c, _ := newCache(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	load := func(ctx context.Context) ([]byte, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return []byte("v"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	first, cancelFirst := context.WithCancel(context.Background())
	firstErr := make(chan error, 1)
	go func() {
		_, err := c.Load(first, Models, "k", time.Minute, load)
		firstErr <- err
	}()
	<-started
	second := make(chan string, 1)
	go func() {
		v, err := c.Load(context.Background(), Models, "k", time.Minute, load)
		if err != nil {
			second <- err.Error()
			return
		}
		second <- string(v)
	}()
	time.Sleep(50 * time.Millisecond)
	cancelFirst()
	if err := <-firstErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that left got %v", err)
	}
	close(release)
	if got := <-second; got != "v" {
		t.Fatalf("the caller that stayed got %q: the first caller's cancel reached the load", got)
	}
}

func TestLoadDropsAResultThatStartedBeforeAPurge(t *testing.T) {
	c, _ := newCache(t)
	ctx := context.Background()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		v, err := c.Load(ctx, Models, "k", time.Minute, func(context.Context) ([]byte, error) {
			close(started)
			<-release
			return []byte("old"), nil
		})
		if err != nil || string(v) != "old" {
			t.Errorf("the caller of the load got %q %v", v, err)
		}
	}()
	<-started
	c.Purge(Models)
	close(release)
	<-done
	if _, ok := c.Get(Models, "k"); ok {
		t.Fatal("a result loaded before the purge was cached after it")
	}
}

func TestLoadWorksWhenTheStoreIsDown(t *testing.T) {
	c, fake := newCache(t)
	fake.StoreNamed("redis").SetDown(true)
	var calls int
	for range 2 {
		v, err := c.Load(context.Background(), Models, "k", time.Minute, func(context.Context) ([]byte, error) { calls++; return []byte("v"), nil })
		if err != nil || string(v) != "v" {
			t.Fatalf("%q %v", v, err)
		}
	}
	if calls != 2 {
		t.Fatalf("loaded %d times: with no cache every call loads", calls)
	}
}

func TestLoadReturnsACopy(t *testing.T) {
	c, _ := newCache(t)
	shared := []byte("abc")
	v, _ := c.Load(context.Background(), Models, "k", time.Minute, func(context.Context) ([]byte, error) { return shared, nil })
	v[0] = 'X'
	if string(shared) != "abc" {
		t.Fatal("the loader's slice was shared with the caller")
	}
}

func TestEveryGroupIsListedOnceAndSharedOnesAreTheDocumentedSix(t *testing.T) {
	var shared []string
	for _, g := range []Group{Settings, APIKeys, Models, Limiter, Response, ToolCalls} {
		shared = append(shared, string(g))
	}
	slices.Sort(shared)
	if got := strings.Join(shared, ","); got != "apikeys,limiter,models,response,settings,toolcalls" {
		t.Fatalf("shared groups = %s", got)
	}
	for _, g := range []Group{Connections, Refresh, OAuth, Overrides} {
		if g.shared() {
			t.Errorf("%s is shared", g)
		}
	}
}

func TestTheSameKeyInTwoGroupsLoadsSeparately(t *testing.T) {
	c, _ := newCache(t)
	release := make(chan struct{})
	results := make(chan string, 2)
	for _, g := range []Group{Settings, Models} {
		go func() {
			v, err := c.Load(context.Background(), g, "same", time.Minute, func(context.Context) ([]byte, error) {
				<-release
				return []byte(g), nil
			})
			if err != nil {
				results <- err.Error()
				return
			}
			results <- string(v)
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	got := []string{<-results, <-results}
	slices.Sort(got)
	if got[0] != "models" || got[1] != "settings" {
		t.Fatalf("results %v: one group got the other's value", got)
	}
}

func TestLoadAfterAPurgeStoresUnderTheNewGeneration(t *testing.T) {
	c, _ := newCache(t)
	c.Purge(Models)
	c.Purge(Models)
	if _, err := c.Load(context.Background(), Models, "k", time.Minute, func(context.Context) ([]byte, error) { return []byte("v"), nil }); err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get(Models, "k"); !ok || string(got) != "v" {
		t.Fatalf("a value loaded after two purges is not readable: %q %v", got, ok)
	}
}

// The Redis driver is built with a nil client when the first connection fails,
// and the facade keeps that driver, so every later call panics.
func TestAPanickingStoreIsAMissNeverACrash(t *testing.T) {
	c, fake := newCache(t)
	fake.StoreNamed("redis").SetPanics(true)
	ctx := context.Background()

	if _, ok := c.Get(Settings, "k"); ok {
		t.Error("Get from a panicking store")
	}
	c.Set(Settings, "k", []byte("v"), time.Minute)
	c.Delete(Settings, "k")
	c.Purge(Settings)
	v, err := c.Load(ctx, Settings, "k", time.Minute, func(context.Context) ([]byte, error) { return []byte("loaded"), nil })
	if err != nil || string(v) != "loaded" {
		t.Fatalf("Load must still run the loader: %q %v", v, err)
	}
}

func TestMemoryGroupsKeepWorkingWhileTheSharedStorePanics(t *testing.T) {
	c, fake := newCache(t)
	fake.StoreNamed("redis").SetPanics(true)
	c.Set(Connections, "k", []byte("v"), time.Minute)
	if got, ok := c.Get(Connections, "k"); !ok || string(got) != "v" {
		t.Fatalf("a credential group depends on the shared store: %q %v", got, ok)
	}
}

func TestTheFirstFailureIsLoggedOnceAndNeverQuotesAValue(t *testing.T) {
	fake := tests.NewCache()
	l := &tests.Log{}
	c := New(fake, WithLog(l))
	fake.StoreNamed("redis").SetPanics(true)
	for range 5 {
		c.Set(Settings, "k", []byte("sk-abcdefghijkl"), time.Minute)
		c.Get(Settings, "k")
	}
	lines := l.Lines()
	if len(lines) != 1 || !strings.Contains(lines[0], "cache") {
		t.Fatalf("log = %q", lines)
	}
	if strings.Contains(lines[0], "sk-abcdefghijkl") || strings.Contains(lines[0], "nil pointer") {
		t.Fatalf("the notice repeats a value or the panic text: %q", lines[0])
	}
}

func TestAFailedWriteIsLoggedOnceToo(t *testing.T) {
	fake := tests.NewCache()
	l := &tests.Log{}
	c := New(fake, WithLog(l))
	fake.StoreNamed("redis").SetDown(true)
	c.Set(Settings, "a", []byte("1"), time.Minute)
	c.Set(Settings, "b", []byte("2"), time.Minute)
	if len(l.Lines()) != 1 {
		t.Fatalf("log = %q", l.Lines())
	}
}

func TestNoLoggerIsSilent(t *testing.T) {
	fake := tests.NewCache()
	c := New(fake)
	fake.StoreNamed("redis").SetPanics(true)
	c.Set(Settings, "k", []byte("v"), time.Minute)
}

// A store that keeps failing is left alone for a cooldown, then tried once
// more, so a stopped Redis costs no call until it may have recovered.
func TestTheBreakerLeavesADownStoreAloneForItsCooldown(t *testing.T) {
	c, fake := newCache(t)
	fake.StoreNamed("redis").SetPanics(true)
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	for range 5 {
		if _, ok := c.Get(Settings, "k"); ok {
			t.Fatal("a panicking store answered")
		}
	}
	if got := fake.StoreNamed("redis").Calls["GetInt64"]; got != 3 {
		t.Fatalf("the store was called %d times; the breaker let it through after three", got)
	}
	now = now.Add(breakerCooldown + time.Second)
	c.Get(Settings, "k")
	if got := fake.StoreNamed("redis").Calls["GetInt64"]; got != 4 {
		t.Fatalf("the store was not tried again after the cooldown: %d", got)
	}
}

func TestASuccessClosesTheBreaker(t *testing.T) {
	c, fake := newCache(t)
	store := fake.StoreNamed("redis")
	store.SetPanics(true)
	c.now = func() time.Time { return time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC) }
	for range 3 {
		c.Get(Settings, "k")
	}
	store.SetPanics(false)
	c.now = func() time.Time { return time.Date(2026, 11, 25, 12, 1, 0, 0, time.UTC) }
	if _, ok := c.Get(Settings, "k"); ok {
		t.Fatal("a value appeared from nowhere")
	}
	c.Set(Settings, "k", []byte("v"), time.Minute)
	if got, ok := c.Get(Settings, "k"); !ok || string(got) != "v" {
		t.Fatalf("the store did not recover: %q %v", got, ok)
	}
}
