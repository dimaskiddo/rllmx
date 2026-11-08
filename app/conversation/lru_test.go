package conversation

import (
	"testing"
	"time"
)

func TestTheOldestEntryIsTheOneDroppedWhenTheTableIsFull(t *testing.T) {
	s, clk := newStore()
	tb := newTable(3)
	for _, k := range []string{"a", "b", "c"} {
		s.store(tb, k, "id-"+k)
		clk.advance(time.Second)
	}
	s.store(tb, "d", "id-d")
	if _, ok := s.lookup(tb, "a"); ok {
		t.Fatal("the oldest entry was kept")
	}
	for _, k := range []string{"b", "c", "d"} {
		if id, ok := s.lookup(tb, k); !ok || id != "id-"+k {
			t.Fatalf("%s: %q %v", k, id, ok)
		}
	}
	if tb.len() != 3 {
		t.Fatalf("%d entries", tb.len())
	}
}

func TestUsingAnEntryMakesItTheNewest(t *testing.T) {
	s, clk := newStore()
	tb := newTable(3)
	for _, k := range []string{"a", "b", "c"} {
		s.store(tb, k, "id-"+k)
		clk.advance(time.Second)
	}
	if _, ok := s.lookup(tb, "a"); !ok {
		t.Fatal("a missing")
	}
	s.store(tb, "d", "id-d")
	if _, ok := s.lookup(tb, "a"); !ok {
		t.Fatal("a recently used entry was dropped")
	}
	if _, ok := s.lookup(tb, "b"); ok {
		t.Fatal("the least recently used entry was kept")
	}
}

func TestExpiredEntriesLeaveFromTheOldEndBeforeAnyLiveOne(t *testing.T) {
	s, clk := newStore()
	tb := newTable(4)
	s.store(tb, "old1", "1")
	s.store(tb, "old2", "2")
	clk.advance(Life + time.Second)
	s.store(tb, "live1", "3")
	s.store(tb, "live2", "4")
	s.store(tb, "live3", "5")
	if tb.len() != 3 {
		t.Fatalf("%d entries; the expired ones should be gone, the live ones kept", tb.len())
	}
	for _, k := range []string{"live1", "live2", "live3"} {
		if _, ok := s.lookup(tb, k); !ok {
			t.Fatalf("%s was dropped for an entry that had not expired", k)
		}
	}
}

func TestStoringAKeyAgainReplacesItWithoutGrowing(t *testing.T) {
	s, _ := newStore()
	tb := newTable(3)
	s.store(tb, "a", "1")
	s.store(tb, "a", "2")
	if tb.len() != 1 || tb.order.Len() != 1 {
		t.Fatalf("map %d list %d", tb.len(), tb.order.Len())
	}
	if id, _ := s.lookup(tb, "a"); id != "2" {
		t.Fatalf("id %q", id)
	}
}

func TestAnExpiredLookupLeavesNothingBehind(t *testing.T) {
	s, clk := newStore()
	tb := newTable(3)
	s.store(tb, "a", "1")
	clk.advance(Life + time.Second)
	if _, ok := s.lookup(tb, "a"); ok {
		t.Fatal("an expired entry was served")
	}
	if tb.len() != 0 || tb.order.Len() != 0 {
		t.Fatalf("map %d list %d", tb.len(), tb.order.Len())
	}
}

func TestManyNewKeysNeverGrowTheTablePastItsLimit(t *testing.T) {
	s, clk := newStore()
	tb := newTable(50)
	for i := range 5000 {
		s.store(tb, itoa(int64(i)), "x")
		clk.advance(time.Millisecond)
		if tb.len() > 50 || tb.order.Len() != tb.len() {
			t.Fatalf("after %d keys: map %d list %d", i, tb.len(), tb.order.Len())
		}
	}
}
