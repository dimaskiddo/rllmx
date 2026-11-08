package providers

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rllmx/app/pipeline"
)

func TestPipelineSourceBuildsOnceAndRetriesAfterAFailure(t *testing.T) {
	calls := 0
	built := &pipeline.Pipeline{}
	s := &PipelineSource{lazy[pipeline.Pipeline]{build: func(context.Context) (*pipeline.Pipeline, error) {
		calls++
		if calls == 1 {
			return nil, ErrStoreNotOpen
		}
		return built, nil
	}}}
	if p, err := s.Get(); p != nil || !errors.Is(err, ErrStoreNotOpen) {
		t.Fatalf("%v %v", p, err)
	}
	for i := 0; i < 3; i++ {
		if p, err := s.Get(); err != nil || p != built {
			t.Fatalf("%v %v", p, err)
		}
	}
	if calls != 2 {
		t.Fatalf("built %d times, want 2 (one failure, one success)", calls)
	}
}

func TestManyFirstRequestsBuildOnceAndLaterOnesTakeNoLock(t *testing.T) {
	var builds atomic.Int32
	gate := make(chan struct{})
	l := &lazy[int]{build: func(context.Context) (*int, error) {
		builds.Add(1)
		<-gate
		n := 5
		return &n, nil
	}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if v, err := l.get(); err != nil || *v != 5 {
				t.Errorf("%v %v", v, err)
			}
		})
	}
	time.Sleep(30 * time.Millisecond)
	close(gate)
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Fatalf("built %d times", n)
	}

	l.mu.Lock() // a held build lock must not stop a built value from being read
	defer l.mu.Unlock()
	done := make(chan struct{})
	go func() { l.get(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a built value waited for the build lock")
	}
}

func TestABuildGetsAContextThatEnds(t *testing.T) {
	l := &lazy[int]{build: func(ctx context.Context) (*int, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("the build context has no deadline")
		}
		return nil, ErrStoreNotOpen
	}}
	if _, err := l.get(); !errors.Is(err, ErrStoreNotOpen) {
		t.Fatalf("err %v", err)
	}
}
