package opencode

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"rllmx/app/llm/executor"
	"rllmx/tests"
)

// noWait makes the overload ramp immediate.
func noWait(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func freeLane(up *tests.Upstream) *OpenCodeFree {
	return &OpenCodeFree{
		Default: &executor.Default{Client: up.Server.Client()},
		Lane:    openCodeLane{chat: up.URL + "/inference/v1/chat/completions"},
		Sleep:   noWait,
	}
}

func TestAnOverloadedFreeLaneIsRetriedWithARamp(t *testing.T) {
	up := tests.NewUpstream(t,
		tests.Reply{Status: http.StatusTooManyRequests, Body: `{"error":{"message":"Backend overloaded, retry"}}`},
		tests.Reply{Status: http.StatusTooManyRequests, Body: `{"error":{"message":"OVERLOADED"}}`},
		tests.Reply{Body: `{"ok":true}`},
	)
	o := freeLane(up)
	resp, err := o.Execute(context.Background(), openCodeFreeReq(up, "glm-5"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusOK || len(up.Calls()) != 3 {
		t.Fatalf("%d after %d calls", resp.Status, len(up.Calls()))
	}
	// The same session id goes out on every try: only a rejection retires it.
	first := up.Calls()[0].Header.Get("x-opencode-session")
	for _, c := range up.Calls()[1:] {
		if c.Header.Get("x-opencode-session") != first {
			t.Fatalf("the session changed between retries")
		}
	}
}

func TestTheOverloadRetryStopsAtEight(t *testing.T) {
	steps := make([]tests.Reply, openCodeOverloadTries)
	for i := range steps {
		steps[i] = tests.Reply{Status: http.StatusTooManyRequests, Body: `overloaded`}
	}
	up := tests.NewUpstream(t, steps...)
	resp, err := freeLane(up).Execute(context.Background(), openCodeFreeReq(up, "glm-5"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != http.StatusTooManyRequests || len(up.Calls()) != openCodeOverloadTries {
		t.Fatalf("%d after %d calls", resp.Status, len(up.Calls()))
	}
}

func TestARejectedSessionGetsAFreshIdOnTheNextTurn(t *testing.T) {
	for name, status := range map[string]int{"429 not overload": http.StatusTooManyRequests, "403": http.StatusForbidden} {
		t.Run(name, func(t *testing.T) {
			up := tests.NewUpstream(t,
				tests.Reply{Status: status, Body: `{"error":"no session"}`},
				tests.Reply{Body: `{}`},
			)
			o := freeLane(up)
			if _, err := o.Execute(context.Background(), openCodeFreeReq(up, "glm-5")); err != nil {
				t.Fatal(err)
			}
			if _, err := o.Execute(context.Background(), openCodeFreeReq(up, "glm-5")); err != nil {
				t.Fatal(err)
			}
			calls := up.Calls()
			if len(calls) != 2 {
				t.Fatalf("%d calls", len(calls))
			}
			if calls[0].Header.Get("x-opencode-session") == calls[1].Header.Get("x-opencode-session") {
				t.Fatal("the rejected id was sent again")
			}
			// One conversation, one generation: the third turn keeps the new id.
			if _, err := o.Execute(context.Background(), openCodeFreeReq(up, "glm-5")); err != nil {
				t.Fatal(err)
			}
			if up.Calls()[2].Header.Get("x-opencode-session") != calls[1].Header.Get("x-opencode-session") {
				t.Fatal("the id changed again without a rejection")
			}
		})
	}
}

func TestTheOverloadWaitEndsWithTheCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	up := tests.NewUpstream(t, tests.Reply{Status: http.StatusTooManyRequests, Body: `overloaded`})
	o := freeLane(up)
	o.Sleep = nil
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := o.Execute(ctx, openCodeFreeReq(up, "glm-5")); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if time.Since(start) > 700*time.Millisecond {
		t.Fatalf("the wait outlived the caller: %v", time.Since(start))
	}
}

func TestTheOverloadRampIsCappedWithJitter(t *testing.T) {
	for try, lo := range map[int]time.Duration{1: openCodeOverloadBase, 5: openCodeOverloadCap, 20: openCodeOverloadCap} {
		if got := openCodeOverloadWait(try); got < lo || got > lo+openCodeOverloadJitter {
			t.Errorf("try %d: %v", try, got)
		}
	}
}

func TestASessionGenerationIsBoundedAndExpires(t *testing.T) {
	now := time.Unix(0, 0)
	var g sessionGens
	if g.get("a", now) != 0 {
		t.Fatal("an unseen seed has a generation")
	}
	g.bump("a", now)
	g.bump("a", now)
	if g.get("a", now) != 2 {
		t.Fatalf("%d", g.get("a", now))
	}
	if g.get("a", now.Add(genTTL+time.Minute)) != 0 {
		t.Fatal("an expired generation was kept")
	}
	for i := 0; i <= genSeedsMax; i++ {
		g.bump(strings.Repeat("x", 8)+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676)), now)
	}
	if len(g.entries) > genSeedsMax {
		t.Fatalf("the map grew to %d", len(g.entries))
	}
}
