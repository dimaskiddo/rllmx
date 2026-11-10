package data

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAGateOpenedBeforeWaitingLetsEveryWaiterThrough(t *testing.T) {
	g := NewGate()
	g.Open()
	for range 3 {
		if !g.Wait(context.Background()) {
			t.Fatal("an open gate refused a waiter")
		}
	}
}

func TestWaitBlocksUntilTheGateOpens(t *testing.T) {
	g := NewGate()
	result := make(chan bool, 1)
	go func() { result <- g.Wait(context.Background()) }()
	select {
	case <-result:
		t.Fatal("Wait returned before the gate opened")
	case <-time.After(30 * time.Millisecond):
	}
	g.Open()
	select {
	case ok := <-result:
		if !ok {
			t.Fatal("the waiter was refused after the gate opened")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter never woke")
	}
}

func TestAFailedGateRefusesWaitersAndStaysFailed(t *testing.T) {
	g := NewGate()
	var wg sync.WaitGroup
	results := make(chan bool, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- g.Wait(context.Background()) }()
	}
	g.Fail()
	wg.Wait()
	close(results)
	for ok := range results {
		if ok {
			t.Fatal("a waiter got through a failed gate")
		}
	}
	g.Open()
	if g.Wait(context.Background()) {
		t.Fatal("a gate that failed opened afterwards")
	}
}

func TestAnOpenGateStaysOpenWhenFailIsCalledLater(t *testing.T) {
	g := NewGate()
	g.Open()
	g.Fail()
	if !g.Wait(context.Background()) {
		t.Fatal("Shutdown after a successful start must not turn the gate back")
	}
}

func TestWaitGivesUpWhenItsContextEnds(t *testing.T) {
	g := NewGate()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan bool, 1)
	go func() { result <- g.Wait(ctx) }()
	cancel()
	select {
	case ok := <-result:
		if ok {
			t.Fatal("a cancelled wait reported ready")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait ignored its context")
	}
}

func TestOpenAndFailAreSafeToRepeatAndToRaceWith(t *testing.T) {
	g := NewGate()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() { defer wg.Done(); g.Open() }()
		go func() { defer wg.Done(); g.Fail() }()
	}
	wg.Wait()
	first := g.Wait(context.Background())
	for range 5 {
		if g.Wait(context.Background()) != first {
			t.Fatal("the gate changed its answer after it was decided")
		}
	}
}
