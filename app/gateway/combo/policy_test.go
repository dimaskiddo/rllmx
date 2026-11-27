package combo

import (
	"context"
	"testing"
	"time"

	"rllmx/app/llm/routing"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

func doc(t *testing.T, raw string) document.Doc {
	t.Helper()
	d, err := document.ParseDoc(raw)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTheStrategyComesFromTheComboThenTheGlobalThenFallback(t *testing.T) {
	global := doc(t, `{"comboStrategy":"round-robin","comboStrategies":{"fast":{"fallbackStrategy":"smart"}}}`)
	if p := Read(global, "fast"); p.Strategy != StrategySmart {
		t.Fatalf("the combo's own strategy lost: %+v", p)
	}
	if p := Read(global, "other"); p.Strategy != StrategyRoundRobin {
		t.Fatalf("the global strategy lost: %+v", p)
	}
	if p := Read(doc(t, `{}`), "x"); p.Strategy != StrategyFallback {
		t.Fatalf("the default lost: %+v", p)
	}
	if p := Read(doc(t, `{"comboStrategies":{"x":{"fallbackStrategy":"nonsense"}}}`), "x"); p.Strategy != StrategyFallback {
		t.Fatalf("a typo should read as fallback: %+v", p)
	}
}

func TestANameWithADotIsStillOneName(t *testing.T) {
	d := doc(t, `{"comboStrategies":{"prod.fast":{"fallbackStrategy":"fusion"}}}`)
	if p := Read(d, "prod.fast"); p.Strategy != StrategyFusion {
		t.Fatalf("%+v", p)
	}
	if p := Read(d, "prod"); p.Strategy != StrategyFallback {
		t.Fatalf("the dot opened a path: %+v", p)
	}
}

func TestTheSmartKnobsLiveOnlyInTheCombosEntry(t *testing.T) {
	d := doc(t, `{"confidenceThreshold":0.1,"comboStrategies":{"s":{"fallbackStrategy":"smart","smartPolicy":"per-turn","sessionSticky":false,"confidenceThreshold":0.65,"routerModel":"openai/gpt-4o","judgeModel":"openai/o3","fusionTuning":{"minPanel":3,"stragglerGraceMs":2000,"panelHardTimeoutMs":30000}}}}`)
	p := Read(d, "s")
	if p.Threshold != 0.65 || p.Sticky || p.SmartPolicy != "per-turn" || p.RouterModel != "openai/gpt-4o" || p.JudgeModel != "openai/o3" {
		t.Fatalf("%+v", p)
	}
	if p.MinPanel != 3 || p.StragglerMS != 2000 || p.PanelMS != 30000 {
		t.Fatalf("fusion knobs: %+v", p)
	}
	// Defaults when the entry names none, and a top-level threshold is ignored.
	q := Read(doc(t, `{"confidenceThreshold":0.1,"comboStrategies":{"s":{"fallbackStrategy":"smart"}}}`), "s")
	if q.Threshold != DefaultThreshold || !q.Sticky || q.RouterModel != defaultRouterModel {
		t.Fatalf("%+v", q)
	}
}

func TestTheRoundRobinLimitIsAtLeastOne(t *testing.T) {
	for raw, want := range map[string]int{
		`{}`:                                    1,
		`{"comboStickyRoundRobinLimit":3}`:      3,
		`{"comboStickyRoundRobinLimit":0}`:      1,
		`{"comboStickyRoundRobinLimit":"many"}`: 1,
		`{"comboStickyRoundRobinLimit":-2}`:     1,
	} {
		if got := Read(doc(t, raw), "x").RotationLimit; got != want {
			t.Errorf("%s: limit = %d, want %d", raw, got, want)
		}
	}
}

func members(models ...string) []routing.Member {
	out := make([]routing.Member, len(models))
	for i, m := range models {
		out[i] = routing.Member{Provider: "p", Model: m}
	}
	return out
}

func names(list []routing.Member) []string {
	out := make([]string, len(list))
	for i, m := range list {
		out[i] = m.Model
	}
	return out
}

func TestRotationMovesTheStartAndAdvancesAfterTheLimit(t *testing.T) {
	r := NewRotation()
	m := members("a", "b", "c")
	for i, want := range [][]string{
		{"a", "b", "c"}, {"a", "b", "c"}, // limit 2: the first start serves twice
		{"b", "c", "a"}, {"b", "c", "a"},
		{"c", "a", "b"},
	} {
		got := names(r.Rotate("fast", m, 2))
		if len(got) != 3 || got[0] != want[0] {
			t.Fatalf("turn %d: %v, want %v", i, got, want)
		}
	}
}

func TestARotationOfOneAdvancesEveryRequest(t *testing.T) {
	r := NewRotation()
	m := members("a", "b")
	for i, want := range []string{"a", "b", "a", "b"} {
		if got := names(r.Rotate("fast", m, 1))[0]; got != want {
			t.Fatalf("turn %d: %s, want %s", i, got, want)
		}
	}
}

func TestTheCapableMemberComesFirstWhateverTheStrategy(t *testing.T) {
	vision := func(m routing.Member) upstream.Capability {
		return upstream.Capability{Vision: m.Model == "vision", VisionSet: true}
	}
	m := members("text", "vision")
	needs := translate.Needs{Vision: true}
	for _, strategy := range []string{StrategyFallback, StrategyRoundRobin} {
		got, ok := Order(context.Background(), Policy{Name: "c", Strategy: strategy, RotationLimit: 1}, m, vision, nil, needs, NewRotation(), nil)
		if !ok || names(got)[0] != "vision" {
			t.Fatalf("%s: %v", strategy, names(got))
		}
	}
	// Without the need the declared order stands.
	got, _ := Order(context.Background(), Policy{Name: "c", Strategy: StrategyFallback}, m, vision, nil, translate.Needs{}, NewRotation(), nil)
	if names(got)[0] != "text" {
		t.Fatalf("%v", names(got))
	}
}

func TestAnOpenProvidersMembersGoLastAndAreNeverDropped(t *testing.T) {
	m := []routing.Member{{Provider: "a", Model: "m1"}, {Provider: "b", Model: "m2"}, {Provider: "a", Model: "m3"}}
	open := func(provider string) bool { return provider == "a" }
	got, ok := Order(context.Background(), Policy{Name: "c", Strategy: StrategyFallback}, m, nil, open, translate.Needs{}, NewRotation(), nil)
	if !ok || len(got) != 3 {
		t.Fatalf("%v", names(got))
	}
	if got[0].Provider != "b" || got[1].Model != "m1" || got[2].Model != "m3" {
		t.Fatalf("order %v", got)
	}
	// Every provider open: the order is the declared one.
	all, _ := Order(context.Background(), Policy{Name: "c", Strategy: StrategyFallback}, m, nil, func(string) bool { return true }, translate.Needs{}, NewRotation(), nil)
	if names(all)[0] != "m1" || len(all) != 3 {
		t.Fatalf("order %v", names(all))
	}
}

func TestAnEmptyComboHasNoOrder(t *testing.T) {
	if _, ok := Order(context.Background(), Policy{}, nil, nil, nil, translate.Needs{}, NewRotation(), nil); ok {
		t.Fatal("an empty combo was ordered")
	}
}

func TestAGlobalFusionStrategyKeepsItsPanelDefaults(t *testing.T) {
	p := Read(doc(t, `{"comboStrategy":"fusion"}`), "chain")
	if p.Strategy != StrategyFusion || p.MinPanel != DefaultMinPanel || p.StragglerMS != DefaultStragglerMS || p.PanelMS != DefaultPanelMS {
		t.Fatalf("%+v", p)
	}
}

func TestRoundRobinRotatesInsideTheCapableTier(t *testing.T) {
	caps := func(m routing.Member) upstream.Capability {
		return upstream.Capability{Vision: m.Model == "v1" || m.Model == "v2", VisionSet: true}
	}
	m := members("text", "v1", "v2")
	needs := translate.Needs{Vision: true}
	r := NewRotation()
	p := Policy{Name: "c", Strategy: StrategyRoundRobin, RotationLimit: 1}
	var leads []string
	for i := 0; i < 4; i++ {
		got, _ := Order(context.Background(), p, m, caps, nil, needs, r, nil)
		if names(got)[2] != "text" {
			t.Fatalf("turn %d: the incapable member is not last: %v", i, names(got))
		}
		leads = append(leads, names(got)[0])
	}
	if leads[0] == leads[1] || leads[0] != leads[2] {
		t.Fatalf("the capable members did not alternate: %v", leads)
	}
}

func visionCaps(m routing.Member) upstream.Capability {
	return upstream.Capability{Vision: m.Model == "vision", VisionSet: true}
}

func TestASmartChoiceCannotOutrankAMemberThatReadsTheTurn(t *testing.T) {
	router := &scriptedRouter{choice: Choice{Model: "text", Confidence: 0.9}}
	smart := &Smart{Router: router, Sticky: NewSticky(), Session: "s1", Now: func() time.Time { return time.Unix(100, 0) }}
	p := Policy{Name: "c", Strategy: StrategySmart, Threshold: 0.5}
	got, _ := Order(context.Background(), p, members("text", "vision"), visionCaps, nil, translate.Needs{Vision: true}, NewRotation(), smart)
	if names(got)[0] != "vision" {
		t.Fatalf("%v", names(got))
	}
}

func TestAnOpenBreakerDoesNotCrossTheCapabilityLine(t *testing.T) {
	m := []routing.Member{{Provider: "vision-p", Model: "vision"}, {Provider: "text-p", Model: "text"}}
	open := func(provider string) bool { return provider == "vision-p" }
	got, _ := Order(context.Background(), Policy{Name: "c", Strategy: StrategyFallback}, m, visionCaps, open, translate.Needs{Vision: true}, NewRotation(), nil)
	if names(got)[0] != "vision" {
		t.Fatalf("%v", names(got))
	}
}

func TestAMemberTheTablesNeverMentionStaysInTheCapableTier(t *testing.T) {
	caps := func(m routing.Member) upstream.Capability {
		if m.Model == "text" {
			return upstream.Capability{VisionSet: true}
		}
		return upstream.Capability{}
	}
	got, _ := Order(context.Background(), Policy{Name: "c", Strategy: StrategyFallback}, members("text", "unknown"), caps, nil, translate.Needs{Vision: true}, NewRotation(), nil)
	if names(got)[0] != "unknown" {
		t.Fatalf("%v", names(got))
	}
}
