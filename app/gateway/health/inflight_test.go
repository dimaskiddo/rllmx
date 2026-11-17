package health

import (
	"sync"
	"testing"
	"time"
)

func TestAReleaseLandsOnceAndDeletesTheSlot(t *testing.T) {
	f := NewInflight(newClock().Now)
	release := f.Start("openai", "gpt", "c1")
	if got := f.Active(); len(got) != 1 || got[0].Count != 1 || got[0].ConnectionID != "c1" {
		t.Fatalf("%+v", got)
	}
	first := f.Generation()
	release(false)
	release(false)
	if got := f.Active(); len(got) != 0 {
		t.Fatalf("the slot stayed: %+v", got)
	}
	if f.Generation() <= first {
		t.Fatal("the release did not move the generation")
	}
}

func TestASecondRequestOnTheSameSlotKeepsItCounted(t *testing.T) {
	f := NewInflight(nil)
	one := f.Start("openai", "gpt", "c1")
	two := f.Start("openai", "gpt", "c1")
	one(true)
	if got := f.Active(); len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("%+v", got)
	}
	if f.ErrorProvider() != "openai" {
		t.Fatal("the failure was not recorded")
	}
	two(false)
	if len(f.Active()) != 0 {
		t.Fatal("the slot stayed")
	}
}

func TestTheErrorProviderGoesStale(t *testing.T) {
	c := newClock()
	f := NewInflight(c.Now)
	f.Start("Claude", "m", "c1")(true)
	if f.ErrorProvider() != "claude" {
		t.Fatal("a fresh failure is not shown")
	}
	c.Add(11 * time.Second)
	if f.ErrorProvider() != "" {
		t.Fatal("an old failure is still shown")
	}
}

func TestActiveIsOrdered(t *testing.T) {
	f := NewInflight(nil)
	_ = f.Start("p2", "m1", "c1")
	_ = f.Start("p1", "m9", "c9")
	_ = f.Start("p1", "m1", "c2")
	got := f.Active()
	want := [][3]string{{"p1", "m1", "c2"}, {"p1", "m9", "c9"}, {"p2", "m1", "c1"}}
	for i, w := range want {
		if got[i].Provider != w[0] || got[i].Model != w[1] || got[i].ConnectionID != w[2] {
			t.Fatalf("%+v", got)
		}
	}
}

func TestConcurrentStartsAndReleasesBalance(t *testing.T) {
	f := NewInflight(nil)
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			release := f.Start("p", "m", "c")
			release(false)
		})
	}
	wg.Wait()
	if len(f.Active()) != 0 {
		t.Fatalf("%+v", f.Active())
	}
}

func TestANilTableAnswersWithoutPanicking(t *testing.T) {
	var f *Inflight
	release := f.Start("p", "m", "c")
	release(true)
	if f.Active() != nil || f.ErrorProvider() != "" || f.Generation() != 0 {
		t.Fatal("a nil table answered something")
	}
}

func TestAnUnusedTableHasNoErrorProvider(t *testing.T) {
	f := NewInflight(nil)
	if f.ErrorProvider() != "" || len(f.Active()) != 0 {
		t.Fatal("an empty table reports state")
	}
}
