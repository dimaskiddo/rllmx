package logbook

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func at(sec int) time.Time { return time.Date(2026, 1, 1, 0, 0, sec, 0, time.UTC) }

func all(b *Book) []Entry {
	lines, _ := b.Snapshot()
	return lines
}

func TestTheRingKeepsTheNewestLinesInOrder(t *testing.T) {
	b := New(3)
	for i := range 5 {
		b.Add("info", at(i), string(rune('a'+i)))
	}
	got := all(b)
	if len(got) != 3 || got[0].Message != "c" || got[2].Message != "e" {
		t.Fatalf("%+v", got)
	}
}

func TestALineCannotForgeARecordOrGrowTheRing(t *testing.T) {
	b := New(4)
	b.Add("info", at(0), "a\x00b\nc\rd")
	if got := all(b); len(got) != 1 || strings.ContainsAny(got[0].Message, "\x00\n\r") {
		t.Fatalf("%+v", got)
	}
	b.Add("info", at(1), strings.Repeat("x", maxLine*2))
	got := all(b)
	if len(got) != 2 || len(got[1].Message) != maxLine {
		t.Fatalf("%d lines, %d bytes", len(got), len(got[1].Message))
	}
}

func TestSinceReturnsOnlyNewerLinesAndNoticesAClear(t *testing.T) {
	b := New(4)
	b.Add("info", at(0), "one")
	b.Add("info", at(1), "two")
	lines, gen := b.Snapshot()
	if len(lines) != 2 {
		t.Fatalf("%+v", lines)
	}
	fresh, g := b.Since(lines[1].Seq, gen)
	if len(fresh) != 0 || g != gen {
		t.Fatalf("%+v %d", fresh, g)
	}
	b.Add("warn", at(2), "three")
	fresh, g = b.Since(lines[1].Seq, gen)
	if len(fresh) != 1 || fresh[0].Message != "three" || fresh[0].Level != "warn" || g != gen {
		t.Fatalf("%+v %d", fresh, g)
	}
	b.Clear()
	fresh, g = b.Since(fresh[0].Seq, gen)
	if len(fresh) != 0 || g == gen {
		t.Fatalf("a clear did not move the generation: %+v %d", fresh, g)
	}
}

func TestAnEmptyBookAnswersEmptyAndNeverBlocks(t *testing.T) {
	var nilBook *Book
	if lines, gen := nilBook.Snapshot(); len(lines) != 0 || gen != 0 {
		t.Fatal("a nil book holds lines")
	}
	nilBook.Add("info", at(0), "ignored")
	nilBook.Clear()
	b := New(0)
	if len(all(b)) != 0 {
		t.Fatal("a new book holds lines")
	}
	b.Add("info", at(0), "x")
	if len(all(b)) != 1 {
		t.Fatalf("a zero size did not take the default: %d", len(all(b)))
	}
}

func TestConcurrentWritersAndReadersStayConsistent(t *testing.T) {
	b := New(16)
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			for j := range 100 {
				b.Add("info", at(j), "x")
				all(b)
				b.Since(0, b.Generation())
				if (i+j)%50 == 0 {
					b.Clear()
				}
			}
		})
	}
	wg.Wait()
	if got := all(b); len(got) > 16 {
		t.Fatalf("the ring grew to %d", len(got))
	}
}
