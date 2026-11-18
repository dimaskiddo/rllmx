package pipeline

import (
	"context"
	"strings"
	"sync"
	"testing"

	"rllmx/app/llm/executor"
	"rllmx/tests"
)

// flightSpy records the slots the pipeline took and released.
type flightSpy struct {
	mu      sync.Mutex
	starts  []string
	failed  []bool
	open    int
	highest int
}

func (f *flightSpy) Start(provider, model, connID string) func(bool) {
	f.mu.Lock()
	f.starts = append(f.starts, provider+"/"+model+"@"+connID)
	f.open++
	if f.open > f.highest {
		f.highest = f.open
	}
	f.mu.Unlock()
	return func(failed bool) {
		f.mu.Lock()
		f.failed = append(f.failed, failed)
		f.open--
		f.mu.Unlock()
	}
}

func (f *flightSpy) seen() (starts []string, failed []bool, open int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.starts...), append([]bool(nil), f.failed...), f.open
}

func TestAnAttemptIsCountedWhileItRunsAndReleasedAfter(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.do("/v1/chat/completions", chatBody)
	starts, failed, open := r.flg.seen()
	want := nodeID + "/gpt-x@"
	if len(starts) != 1 || !strings.HasPrefix(starts[0], want) || len(starts[0]) == len(want) {
		t.Fatalf("starts %v", starts)
	}
	if len(failed) != 1 || failed[0] || open != 0 {
		t.Fatalf("a served attempt: %v open=%d", failed, open)
	}
}

func TestAFailedAttemptIsReleasedAsFailedAndTheNextIsNot(t *testing.T) {
	r := newRig(t,
		tests.Reply{Status: 500, ContentType: "application/json", Body: `{"error":{"message":"down"}}`},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	r.do("/v1/chat/completions", chatBody)
	starts, failed, open := r.flg.seen()
	if len(starts) != 2 || starts[0] == starts[1] {
		t.Fatalf("each account needs its own slot: %v", starts)
	}
	if len(failed) != 2 || !failed[0] || failed[1] || open != 0 {
		t.Fatalf("failed %v open=%d", failed, open)
	}
}

type panickingExecutor struct{}

func (panickingExecutor) Execute(context.Context, executor.Request) (executor.Response, error) {
	panic("executor blew up")
}

func TestAnAttemptThatPanicsStillReleasesItsSlot(t *testing.T) {
	r := newRig(t)
	r.conn(nodeID, "k1")
	r.p.d.Executor = panickingExecutor{}
	func() {
		defer func() { _ = recover() }()
		r.run("/v1/chat/completions", chatBody)
	}()
	if _, _, open := r.flg.seen(); open != 0 {
		t.Fatalf("%d slots are still counted after a panic", open)
	}
}
