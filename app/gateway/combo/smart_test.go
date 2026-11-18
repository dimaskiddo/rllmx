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
	model  string
}

func (r *scriptedRouter) Choose(_ context.Context, model, _ string, _ []string) (Choice, error) {
	r.asked++
	r.model = model
	return r.choice, r.err
}

func TestASmartChoiceLeadsAndIsRemembered(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "b", Confidence: 0.9}}
	sticky := NewSticky()
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true}
	smart := &Smart{Router: router, Sticky: sticky, Session: "s1", Now: func() time.Time { return time.Unix(100, 0) }}
	out, _ := Order(context.Background(), p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
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
	out, _ := Order(context.Background(), p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
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
		out, _ := Order(context.Background(), p, members("a", "b"), nil, nil, needsNone(), NewRotation(), &Smart{Router: r, Sticky: NewSticky(), Session: "s1"})
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
	out, _ := Order(context.Background(), p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
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
	out, _ := Order(context.Background(), p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if router.asked != 1 {
		t.Fatalf("the router was asked %d times", router.asked)
	}
	if names(out)[0] != "c" {
		t.Fatalf("%v", names(out))
	}
	// Per-turn asks again on the next turn, and the choice leads each time.
	out, _ = Order(context.Background(), p, members("a", "b", "c"), nil, nil, needsNone(), NewRotation(), smart)
	if router.asked != 2 || names(out)[0] != "c" {
		t.Fatalf("asked %d, order %v", router.asked, names(out))
	}
}

func TestAStickyChoiceThatLeftTheComboIsForgotten(t *testing.T) {
	sticky := NewSticky()
	sticky.Put("fast", "s1", "gone", time.Now())
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, Sticky: true}
	out, _ := Order(context.Background(), p, members("a", "b"), nil, nil, needsNone(), NewRotation(), &Smart{Router: nil, Sticky: sticky, Session: "s1"})
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

func TestTheRouterIsAskedOnTheCombosOwnModel(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "b", Confidence: 0.9}}
	p := Policy{Name: "fast", Strategy: StrategySmart, Threshold: 0.8, RouterModel: "openai/gpt-4o"}
	Order(context.Background(), p, members("a", "b"), nil, nil, needsNone(), NewRotation(), &Smart{Router: router, Sticky: NewSticky()})
	if router.model != "openai/gpt-4o" {
		t.Fatalf("asked %q", router.model)
	}
}

type ctxRouter struct{ got context.Context }

func (r *ctxRouter) Choose(ctx context.Context, _, _ string, _ []string) (Choice, error) {
	r.got = ctx
	return Choice{}, ErrNoChoice
}

func TestTheRouterRunsOnTheRequestsContext(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "mine")
	router := &ctxRouter{}
	p := Policy{Name: "fast", Strategy: StrategySmart}
	Order(ctx, p, members("a", "b"), nil, nil, needsNone(), NewRotation(), &Smart{Router: router, Sticky: NewSticky()})
	if router.got == nil || router.got.Value(key{}) != "mine" {
		t.Fatal("the router did not get the request's context")
	}
}

func TestAFullStickyStoreDropsItsOldestWriteAndKeepsARewrittenOne(t *testing.T) {
	sticky := NewSticky()
	at := time.Unix(1000, 0)
	for i := 0; i < stickyLimit; i++ {
		sticky.Put("c", "s"+strconv.Itoa(i), "m", at)
	}
	// s0 is the oldest; writing it again makes s1 the oldest.
	sticky.Put("c", "s0", "m2", at)
	sticky.Put("c", "new", "m", at)
	if sticky.len() != stickyLimit {
		t.Fatalf("%d entries", sticky.len())
	}
	if got := sticky.Get("c", "s0", at); got != "m2" {
		t.Fatalf("the rewritten entry was dropped: %q", got)
	}
	if got := sticky.Get("c", "s1", at); got != "" {
		t.Fatalf("the oldest write was kept: %q", got)
	}
}
