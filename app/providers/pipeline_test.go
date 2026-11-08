package providers

import (
	"errors"
	"testing"

	"rllmx/app/pipeline"
)

func TestPipelineSourceBuildsOnceAndRetriesAfterAFailure(t *testing.T) {
	calls := 0
	built := &pipeline.Pipeline{}
	s := &PipelineSource{lazy[pipeline.Pipeline]{build: func() (*pipeline.Pipeline, error) {
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
