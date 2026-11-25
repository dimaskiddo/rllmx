package pipeline

import (
	"math"
	"sync"
	"time"
)

// Semantic caps: a bucket is one request shape, so a few hundred variants are
// plenty; the total bounds the whole process.
const (
	semanticBucketCap = 256
	semanticTotalCap  = 10000
)

// semanticEntry is one stored request's unit-length embedding and the exact
// cache key its answer lives under.
type semanticEntry struct {
	key string
	vec []float32
	at  time.Time
}

// SemanticIndex matches a request against the requests already cached, by
// cosine similarity. It is per process and holds no answer and no text.
type SemanticIndex struct {
	mu      sync.Mutex
	buckets map[string][]semanticEntry
	total   int
	now     func() time.Time
}

// NewSemanticIndex returns an empty index.
func NewSemanticIndex() *SemanticIndex {
	return &SemanticIndex{buckets: map[string][]semanticEntry{}, now: time.Now}
}

// Lookup answers the closest entry at or above threshold. An entry whose exact
// key expired is dropped by the caller.
func (s *SemanticIndex) Lookup(bucket string, vec []float32, threshold float64) (string, float64, bool) {
	if s == nil || len(vec) == 0 {
		return "", 0, false
	}
	// Stored vectors are unit length, so the query must be too: a longer one
	// would score past the threshold against an unrelated entry.
	vec = normalize(vec)
	if len(vec) == 0 {
		return "", 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	best, bestScore := "", 0.0
	for _, e := range s.buckets[bucket] {
		score := dot(vec, e.vec)
		if score > bestScore {
			best, bestScore = e.key, score
		}
	}
	if bestScore < threshold {
		return "", bestScore, false
	}
	return best, bestScore, true
}

// Add stores one entry, evicting the bucket's oldest and, at the total cap, the
// oldest across buckets.
func (s *SemanticIndex) Add(bucket, key string, vec []float32) {
	if s == nil || key == "" || len(vec) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	norm := normalize(vec)
	if len(norm) == 0 {
		return
	}
	list := append(s.buckets[bucket], semanticEntry{key: key, vec: norm, at: s.now()})
	s.total++
	if over := len(list) - semanticBucketCap; over > 0 {
		list = list[over:]
		s.total -= over
	}
	s.buckets[bucket] = list
	if s.total > semanticTotalCap {
		s.evictOldest()
	}
}

// Drop forgets one exact key, for an answer that expired.
func (s *SemanticIndex) Drop(key string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for bucket, list := range s.buckets {
		kept := list[:0]
		for _, e := range list {
			if e.key != key {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(s.buckets, bucket)
			continue
		}
		s.buckets[bucket] = kept
	}
	s.total = 0
	for _, list := range s.buckets {
		s.total += len(list)
	}
}

// Purge forgets everything, as a settings write does for the exact cache.
func (s *SemanticIndex) Purge() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buckets = map[string][]semanticEntry{}
	s.total = 0
}

// evictOldest removes the single oldest entry across buckets. The caller holds
// the lock.
func (s *SemanticIndex) evictOldest() {
	oldestBucket, oldest := "", time.Time{}
	for bucket, list := range s.buckets {
		if len(list) == 0 {
			continue
		}
		if oldestBucket == "" || list[0].at.Before(oldest) {
			oldestBucket, oldest = bucket, list[0].at
		}
	}
	if oldestBucket == "" {
		return
	}
	if rest := s.buckets[oldestBucket][1:]; len(rest) > 0 {
		s.buckets[oldestBucket] = rest
	} else {
		delete(s.buckets, oldestBucket)
	}
	s.total--
}

// normalize returns the unit-length copy of a vector.
func normalize(vec []float32) []float32 {
	var sum float64
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 || math.IsInf(sum, 0) || math.IsNaN(sum) {
		return nil
	}
	length := math.Sqrt(sum)
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(float64(v) / length)
	}
	return out
}

// dot is the cosine similarity of two unit vectors.
func dot(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
