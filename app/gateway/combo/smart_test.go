package combo

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"rllmx/app/llm/translate"
)

// scriptedRouter answers every question with one choice, or an error.
type scriptedRouter struct {
	choice Choice
	err    error
	asked  int
}

func (r *scriptedRouter) Choose(context.Context, string, []string) (Choice, error) {
	r.asked++
	return r.choice, r.err
}

func TestASmartChoiceLeadsAndIsRemembered(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "b", Confidence: 0.9}}
	sticky := NewSticky()
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true}
	smart := &Smart{Router: router, Sticky: sticky, Session: "s1", Now: func() time.Time { return time.Unix(100, 0) }}
	out, _ := Order(p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if names(out)[0] != "b" {
		t.Fatalf("%v", names(out))
	}
	if got := sticky.Get("fast", "s1", time.Unix(100, 0)); got != "b" {
		t.Fatalf("sticky = %q", got)
	}
}

func TestASmartChoiceBelowTheThresholdKeepsTheOrder(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "b", Confidence: 0.5}}
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true}
	smart := &Smart{Router: router, Sticky: NewSticky(), Session: "s1"}
	out, _ := Order(p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if names(out)[0] != "a" {
		t.Fatalf("%v", names(out))
	}
}

func TestAFailingRouterKeepsTheOrder(t *testing.T) {
	for _, r := range []*scriptedRouter{
		{err: errors.New("down")},
		{choice: Choice{Model: "notin", Confidence: 0.99}},
	} {
		p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true}
		out, _ := Order(p, members("a", "b"), nil, nil, needsNone(), NewRotation(), &Smart{Router: r, Sticky: NewSticky(), Session: "s1"})
		if names(out)[0] != "a" {
			t.Fatalf("%v", names(out))
		}
	}
}

func TestAStrictPolicyKeepsTheStickyChoiceWithoutAsking(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "c", Confidence: 0.99}}
	sticky := NewSticky()
	sticky.Put("fast", "s1", "b", time.Now())
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true, SmartPolicy: "strict"}
	smart := &Smart{Router: router, Sticky: sticky, Session: "s1"}
	out, _ := Order(p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if names(out)[0] != "b" || router.asked != 0 {
		t.Fatalf("order %v, router asked %d times", names(out), router.asked)
	}
}

func TestAPerTurnPolicyReasksEveryTurn(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "c", Confidence: 0.99}}
	sticky := NewSticky()
	sticky.Put("fast", "s1", "b", time.Now())
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true, SmartPolicy: "per-turn"}
	smart := &Smart{Router: router, Sticky: sticky, Session: "s1"}
	out, _ := Order(p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if router.asked != 1 {
		t.Fatalf("the router was asked %d times", router.asked)
	}
	if names(out)[0] != "c" {
		t.Fatalf("%v", names(out))
	}
	// Per-turn asks again on the next turn, and the choice leads each time.
	out, _ = Order(p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if router.asked != 2 || names(out)[0] != "c" {
		t.Fatalf("asked %d, order %v", router.asked, names(out))
	}
}

func TestAStickyChoiceThatLeftTheComboIsForgotten(t *testing.T) {
	sticky := NewSticky()
	sticky.Put("fast", "s1", "gone", time.Now())
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true}
	out, _ := Order(p, members("a", "b"), nil, nil, needsNone(), NewRotation(), &Smart{Router: nil, Sticky: sticky, Session: "s1"})
	if names(out)[0] != "a" {
		t.Fatalf("%v", names(out))
	}
}

func TestTheStickyStoreAgesAndBoundsItself(t *testing.T) {
	sticky := NewSticky()
	sticky.Put("fast", "s1", "a", time.Unix(0, 0))
	sticky.Put("fast", "s1", "b", time.Unix(100, 0))
	// Within the life the last choice wins; past it, nothing.
	if got := sticky.Get("fast", "s1", time.Unix(1000, 0)); got != "b" {
		t.Fatalf("got %q", got)
	}
	if got := sticky.Get("fast", "s1", time.Unix(1000, 0).Add(stickyLife+time.Second)); got != "" {
		t.Fatalf("aged entry %q", got)
	}
	// A session with nothing remembers nothing.
	if got := sticky.Get("fast", "other", time.Unix(100, 0)); got != "" {
		t.Fatalf("another session %q", got)
	}
	// The store stays bounded.
	for i := 0; i < stickyLimit+10; i++ {
		sticky.Put("fast", "s", string(rune('a'+i%26))+strconv.Itoa(i), time.Unix(int64(i), 0))
	}
	if n := sticky.len(); n > stickyLimit {
		t.Fatalf("store holds %d entries", n)
	}
	// Drop forgets one combo only.
	sticky.Put("other", "s1", "x", time.Unix(50, 0))
	sticky.Drop("fast")
	if sticky.len() != 1 {
		t.Fatalf("after drop: %d entries", sticky.len())
	}
}

func needsNone() translate.Needs { return translate.Needs{} }
