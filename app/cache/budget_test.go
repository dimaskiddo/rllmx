package cache

import (
	"sync"
	"testing"
	"time"
)

func TestBudgetStopsAtTheEntryLimit(t *testing.T) {
	b := &Budget{MaxEntries: 2, MaxBytes: 1 << 20}
	for i := 0; i < 2; i++ {
		if !b.Reserve(1, time.Minute) {
			t.Fatalf("entry %d of 2 was refused", i+1)
		}
	}
	if b.Reserve(1, time.Minute) {
		t.Fatal("a third entry passed the entry limit")
	}
}

func TestBudgetStopsAtTheByteLimit(t *testing.T) {
	b := &Budget{MaxEntries: 100, MaxBytes: 10}
	if !b.Reserve(6, time.Minute) {
		t.Fatal("6 of 10 bytes was refused")
	}
	if b.Reserve(5, time.Minute) {
		t.Fatal("11 of 10 bytes was admitted")
	}
	if !b.Reserve(4, time.Minute) {
		t.Fatal("exactly 10 of 10 bytes was refused")
	}
}

func TestBudgetReleasesWhatHasExpired(t *testing.T) {
	now := time.Unix(1000, 0)
	b := &Budget{MaxEntries: 1, MaxBytes: 10, Now: func() time.Time { return now }}
	if !b.Reserve(10, time.Minute) {
		t.Fatal("refused")
	}
	if b.Reserve(1, time.Minute) {
		t.Fatal("admitted while full")
	}
	now = now.Add(time.Minute)
	if !b.Reserve(10, time.Minute) {
		t.Fatal("an expired entry still held its place")
	}
}

func TestANilBudgetAdmitsEverythingAndANegativeSizeIsRefused(t *testing.T) {
	var none *Budget
	if !none.Reserve(1<<40, time.Hour) {
		t.Fatal("a nil budget refused")
	}
	if (&Budget{MaxEntries: 1, MaxBytes: 10}).Reserve(-1, time.Minute) {
		t.Fatal("a negative size was admitted")
	}
}

func TestBudgetIsSafeForConcurrentUse(t *testing.T) {
	b := &Budget{MaxEntries: 50, MaxBytes: 1 << 20}
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Reserve(10, time.Minute) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 50 {
		t.Fatalf("admitted %d, want exactly the limit of 50", admitted)
	}
}
