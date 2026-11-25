package pipeline

import (
	"strconv"
	"testing"
)

func TestALongQueryVectorScoresLikeAUnitOne(t *testing.T) {
	idx := NewSemanticIndex()
	idx.Add("b", "k1", []float32{1, 0})
	if _, score, ok := idx.Lookup("b", []float32{3, 4}, 0.9); ok || score > 0.61 {
		t.Fatalf("a query of length 5 scored %.2f against an unrelated entry, hit=%v", score, ok)
	}
	if key, score, ok := idx.Lookup("b", []float32{50, 0}, 0.99); !ok || key != "k1" || score < 0.999 {
		t.Fatalf("the same direction at length 50: %q %.3f %v", key, score, ok)
	}
}

func TestATrimmedBucketLowersTheTotalAndAnEmptiedOneIsForgotten(t *testing.T) {
	idx := NewSemanticIndex()
	for i := range semanticBucketCap + 40 {
		idx.Add("busy", "k"+strconv.Itoa(i), []float32{1, float32(i + 1)})
	}
	if idx.total != semanticBucketCap {
		t.Fatalf("total %d after a bucket held %d", idx.total, semanticBucketCap)
	}
	idx.Add("one", "solo", []float32{1, 2})
	idx.Drop("solo")
	if _, left := idx.buckets["one"]; left {
		t.Fatal("an emptied bucket stayed in the map")
	}
}

func TestEvictingTheOldestEntryOfALoneBucketForgetsTheBucket(t *testing.T) {
	idx := NewSemanticIndex()
	idx.Add("lone", "k", []float32{1, 2})
	idx.evictOldest()
	if _, left := idx.buckets["lone"]; left || idx.total != 0 {
		t.Fatalf("buckets %d total %d", len(idx.buckets), idx.total)
	}
}
