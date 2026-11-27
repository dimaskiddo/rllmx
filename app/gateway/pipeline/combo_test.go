package pipeline

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/combo"
	"rllmx/app/llm/routing"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

// comboRig is a rig whose resolver serves one combo over the given members, with
// a connection for every provider named.
func comboRig(t *testing.T, members []string, steps ...tests.Reply) *rig {
	t.Helper()
	r := newRig(t, steps...)
	r.p.d.Resolver = &routing.Resolver{Registry: r.p.d.Registry, Snapshot: routing.NewSnapshot(source{
		combos:       []string{"chain"},
		comboMembers: []repository.Combo{{Name: "chain", Models: members}},
	}, nil)}
	return r
}

const comboAsk = `{"model":"chain","messages":[{"role":"user","content":"hi"}]}`

func TestAComboMemberThatAnswersEndsTheSearch(t *testing.T) {
	r := comboRig(t, []string{"forced/fm", "gem/g-vision"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if calls := len(r.up.Calls()); calls != 1 {
		t.Fatalf("%d upstream calls, want the first member only", calls)
	}
	if row := r.last(); row.ComboName != "chain" || row.Provider != "forced" || row.Attempt != 1 {
		t.Fatalf("%+v", row)
	}
}

func TestAComboSkipsAMemberItCannotReach(t *testing.T) {
	// The first member names a provider the registry does not have; the second
	// answers.
	r := comboRig(t, []string{"nowhere/gpt-x", "forced/fm"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "forced" {
		t.Fatalf("%+v", row)
	}
}

func TestAComboLeavesOneUsageRowWhateverItTried(t *testing.T) {
	r := comboRig(t, []string{"nowhere/gpt-x", "forced/fm"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if rows := len(r.rows.All()); rows != 1 {
		t.Fatalf("%d usage rows, want exactly one", rows)
	}
}

func TestAComboOfProvidersWithNoAccountRefusesTheRequest(t *testing.T) {
	// The members resolve, but neither provider has a connection: the search ends
	// with the first member's own refusal, not a 200.
	r := comboRig(t, []string{"nowhere/gpt-x", "elsewhere/gpt-x"})
	if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status >= 200 && out.Status < 300 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAComboThatNamesItselfIsRefused(t *testing.T) {
	r := newRig(t)
	r.p.d.Resolver = &routing.Resolver{Registry: r.p.d.Registry, Snapshot: routing.NewSnapshot(source{
		combos:       []string{"loop"},
		comboMembers: []repository.Combo{{Name: "loop", Models: []string{"forced/fm", "loop"}}},
	}, nil)}
	out, _ := r.do("/v1/chat/completions", `{"model":"loop","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 400 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAComboReadsAMemberFailure(t *testing.T) {
	cases := map[int]memberClass{
		400: memberRequestScoped, 404: memberRequestScoped, 422: memberRequestScoped,
		401: memberFallback, 402: memberFallback, 403: memberFallback, 429: memberFallback,
		502: memberRetry, 503: memberRetry, 504: memberRetry,
		500: memberFallback, 505: memberFallback,
	}
	for status, want := range cases {
		if got := classifyMember(&accountFail{status: status}); got != want {
			t.Errorf("%d: class %d, want %d", status, got, want)
		}
	}
}

func TestAnUnreadableSettingLeavesAComboInDeclaredOrder(t *testing.T) {
	// The strategy comes from the settings row; with none, the first member is
	// tried first and answers, so the row names it.
	r := comboRig(t, []string{"forced/fm", "gem/g-vision"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Rotation = combo.NewRotation()
	if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "forced" {
		t.Fatalf("%+v", row)
	}
}

func TestAFallbackComboKeepsItsDeclaredOrder(t *testing.T) {
	// gem is listed first but cannot answer in this rig, so a fallback combo
	// always reaches forced; the assertion is that the order did not rotate it to
	// the front on a later request.
	r := comboRig(t, []string{"gem/g-vision", "forced/fm"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Rotation = combo.NewRotation()
	r.p.d.Settings = tests.Settings{JSON: `{"comboStrategy":"fallback"}`}
	for range 2 {
		if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status != 200 {
			t.Fatalf("%d %s", out.Status, out.Body)
		}
		if got := r.last().Provider; got != "forced" {
			t.Fatalf("the fallback order moved: %s", got)
		}
	}
}

func TestAComboStrategyFromSettingsRotatesTheStart(t *testing.T) {
	// gem is listed first but cannot answer in this rig; forced answers. With
	// round-robin the second request starts at forced and stops after one call;
	// with fallback it starts at gem again and needs two.
	r := comboRig(t, []string{"gem/g-vision", "forced/fm"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Rotation = combo.NewRotation()
	r.p.d.Settings = tests.Settings{JSON: `{"comboStrategy":"round-robin"}`}
	for i, want := range []int{2, 1} {
		before := len(r.up.Calls())
		if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status != 200 {
			t.Fatalf("turn %d: %d %s", i, out.Status, out.Body)
		}
		if got := len(r.up.Calls()) - before; got != want {
			t.Fatalf("turn %d made %d upstream calls, want %d: the settings strategy was not read", i, got, want)
		}
	}
}

func TestAFallbackStrategyFromSettingsKeepsTheStart(t *testing.T) {
	r := comboRig(t, []string{"gem/g-vision", "forced/fm"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Rotation = combo.NewRotation()
	r.p.d.Settings = tests.Settings{JSON: `{"comboStrategy":"fallback"}`}
	for i := range 2 {
		before := len(r.up.Calls())
		if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status != 200 {
			t.Fatalf("turn %d: %d %s", i, out.Status, out.Body)
		}
		if got := len(r.up.Calls()) - before; got != 2 {
			t.Fatalf("turn %d made %d upstream calls, want 2: fallback did not keep the start", i, got)
		}
	}
}

func TestAComboMovesOnWhenAMemberHasNoConnection(t *testing.T) {
	// wire is in the registry but has no connection here; forced answers. The
	// no-account answer must not be mistaken for the member's answer.
	r := comboRig(t, []string{"wire/short", "forced/fm"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "forced" {
		t.Fatalf("the second member should have answered: %+v", row)
	}
}

func TestASmartComboKeepsTheOrderWhenTheRouterIsNotAvailable(t *testing.T) {
	// The policy names a router model this deployment cannot reach; the request
	// still answers through the declared order.
	r := comboRig(t, []string{"forced/fm", "gem/g-vision"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Rotation = combo.NewRotation()
	r.p.d.Sticky = combo.NewSticky()
	r.p.d.Settings = tests.Settings{JSON: `{"comboStrategies":{"chain":{"fallbackStrategy":"smart","routerModel":"nonexistent/router-model"}}}`}
	if out, _ := r.do("/v1/chat/completions", comboAsk); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "forced" {
		t.Fatalf("the declared order was lost: %+v", row)
	}
}

// fusionRig serves a combo through the fusion strategy over the given members.
func fusionRig(t *testing.T, settings string, members []string, steps ...tests.Reply) *rig {
	t.Helper()
	r := comboRig(t, members, steps...)
	r.p.d.Rotation = combo.NewRotation()
	r.p.d.Sticky = combo.NewSticky()
	r.p.d.Settings = tests.Settings{JSON: settings}
	return r
}

const fusionAsk = `{"model":"chain","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`

func TestAFusionPanelQuorumAnswers(t *testing.T) {
	// Both members answer (every reply is a JSON answer, so the panel's call
	// order does not matter); the quorum is met and the judge combines them.
	r := fusionRig(t, `{"comboStrategies":{"chain":{"fallbackStrategy":"fusion","judgeModel":"wire/short"}}}`,
		[]string{"forced/fm", "wire/short"},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	out, _ := r.do("/v1/chat/completions", fusionAsk)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if rows := len(r.rows.All()); rows != 1 {
		t.Fatalf("%d usage rows, want exactly one", rows)
	}
	// The judge asked wire by name: its model appears in an upstream body, with
	// the anonymised answers and without a model attribution for the panel.
	// The judge is the only call whose body carries the panel's answers, and it
	// runs wire's model (upstream id `vendor/long-name`) with the client's tools.
	sawJudge, sawAnonymised, sawTools := false, false, false
	for _, c := range r.up.Calls() {
		body := string(c.Body)
		if !strings.Contains(body, "Combine these answers") {
			continue
		}
		sawJudge = strings.Contains(body, `"model":"vendor/long-name"`)
		// The answers are numbered with no model named beside them.
		sawAnonymised = strings.Contains(body, "Answer 1:"+`\`+"nhello")
		sawTools = strings.Contains(body, `"tools"`)
	}
	if !sawJudge {
		t.Fatal("the judge was not asked with the policy's model")
	}
	if !sawAnonymised {
		t.Fatal("the judge did not see the anonymised answers")
	}
	if !sawTools {
		t.Fatal("the judge lost the client's tools")
	}
	// The panel members were asked without tools: no panel body carries them.
	for _, c := range r.up.Calls() {
		body := string(c.Body)
		if strings.Contains(body, "Combine these answers") {
			continue // the judge keeps its tools on purpose
		}
		if strings.Contains(body, `"tools"`) {
			t.Fatalf("a panel member saw the client's tools: %s", body)
		}
	}
}

func TestAFusionWithNoAnswerIsAServiceError(t *testing.T) {
	r := fusionRig(t, `{"comboStrategies":{"chain":{"fallbackStrategy":"fusion"}}}`,
		[]string{"nowhere/gpt-x", "elsewhere/gpt-x"})
	out, _ := r.do("/v1/chat/completions", fusionAsk)
	if out.Status != http.StatusServiceUnavailable || !strings.Contains(string(out.Body), "All fusion panel models failed") {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAFusionWithOneAnswerRerunsThatMemberWithoutAJudge(t *testing.T) {
	// Only forced answers; the judge is named as wire, which must never be called
	// when a single answer has to be re-run with the client's own request.
	r := fusionRig(t, `{"comboStrategies":{"chain":{"fallbackStrategy":"fusion","judgeModel":"wire/short"}}}`,
		[]string{"forced/fm", "gem/g-vision"},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()},
		tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.conn("wire", "k3")
	out, _ := r.do("/v1/chat/completions", fusionAsk)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	for _, c := range r.up.Calls() {
		if strings.Contains(string(c.Body), `"model":"short"`) {
			t.Fatalf("the judge was called for a single answer: %s", c.Body)
		}
	}
}

func TestTheJudgeIsThePolicyModelOrTheFirstMember(t *testing.T) {
	r := comboRig(t, []string{"forced/fm", "wire/short"})
	c := &call{p: r.p}
	first := routing.Member{Provider: "forced", Model: "fm"}
	got := c.judgeMember(context.Background(), combo.Policy{JudgeModel: "wire/short"}, first)
	if got.Provider != "wire" || got.Model != "short" {
		t.Fatalf("the named judge was lost: %+v", got)
	}
	got = c.judgeMember(context.Background(), combo.Policy{}, first)
	if got != first {
		t.Fatalf("without a judge the first member answers: %+v", got)
	}
	// A judge name resolves like any other model name, so its provider and model
	// come from the resolver; only an empty name falls back to the first member.
	got = c.judgeMember(context.Background(), combo.Policy{JudgeModel: "wire/short"}, routing.Member{Provider: "forced", Model: "fm"})
	if got.Provider != "wire" {
		t.Fatalf("the judge's provider was not resolved: %+v", got)
	}
}

func TestAComboMovesOnWhenTheFirstMemberAnswersNothing(t *testing.T) {
	// The first member's answer carries no content; the second answers.
	r := comboRig(t, []string{"forced/fm", "wire/short"},
		tests.Reply{ContentType: "application/json", Body: `{"choices":[]}`},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "wire" {
		t.Fatalf("the empty answer was kept: %+v", row)
	}
	if rows := len(r.rows.All()); rows != 1 {
		t.Fatalf("%d usage rows", rows)
	}
}

func TestAnEmptyLastMemberOutranksAnEarlierRefusal(t *testing.T) {
	// The first member refuses the request, which is kept as a candidate answer;
	// the last member answers with no content, and that is the answer the client
	// gets rather than the first member's refusal.
	r := comboRig(t, []string{"wire/short", "forced/fm"},
		tests.Reply{Status: 400, Body: `{"error":{"message":"bad"}}`},
		tests.Reply{ContentType: "application/json", Body: `{"choices":[]}`})
	r.conn("wire", "k3")
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != http.StatusBadGateway {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAComboReturnsAnEmptyAnswerThatIsTheLastMember(t *testing.T) {
	r := comboRig(t, []string{"forced/fm"},
		tests.Reply{ContentType: "application/json", Body: `{"choices":[]}`})
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 502 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAComboMovesOnWhenTheFirstMembersAccountsAreLocked(t *testing.T) {
	// A 429 locks the first member's only account; the next member answers
	// instead of the lock ending the search.
	r := comboRig(t, []string{"forced/fm", "wire/short"},
		tests.Reply{Status: 429, Body: `{"error":{"message":"slow down"}}`},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 200 || gjson.GetBytes(out.Body, "choices.0.message.content").String() != "hello" {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	row := r.last()
	if row.Provider != "wire" || row.ErrorClass != "none" {
		t.Fatalf("the row names the failed member or its class: %+v", row)
	}
}

func TestAComboThatFailsEverywhereAnswersAFailureNotASuccess(t *testing.T) {
	r := comboRig(t, []string{"forced/fm", "gem/g-vision"},
		tests.Reply{Status: 429, Body: `{"error":{"message":"first"}}`},
		tests.Reply{Status: 401, Body: `{"error":{"message":"second"}}`})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status < 400 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if calls := len(r.up.Calls()); calls != 2 {
		t.Fatalf("%d upstream calls, want both members tried", calls)
	}
}

func TestAComboOfOneMediaMemberAnswersAnErrorNotStatusZero(t *testing.T) {
	r := comboRig(t, []string{"forced/fm-img"})
	r.conn("forced", "k1")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status < 400 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestAFusionRowCarriesThePanelsAndTheJudgesSpend(t *testing.T) {
	r := fusionRig(t, `{"comboStrategies":{"chain":{"fallbackStrategy":"fusion","judgeModel":"wire/short"}}}`,
		[]string{"forced/fm", "wire/short"},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	out, _ := r.do("/v1/chat/completions", fusionAsk)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	row := r.last()
	if row.ComboName != "chain" || row.Provider != "wire" || row.Model != "short" {
		t.Fatalf("the row does not name the combo and the judge: %+v", row)
	}
	// Two panel answers and the judge, ten and five tokens each.
	if row.PromptTokens != 30 || row.CompletionTokens != 15 {
		t.Fatalf("the row holds %d + %d tokens, want the three calls' 30 + 15", row.PromptTokens, row.CompletionTokens)
	}
}

func TestAGlobalFusionComboServesWithNoEntryOfItsOwn(t *testing.T) {
	r := fusionRig(t, `{"comboStrategy":"fusion"}`, []string{"forced/fm", "wire/short"},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	if out, _ := r.do("/v1/chat/completions", fusionAsk); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
}

func TestTheRowKeepsTheClassOfTheFailureTheClientGets(t *testing.T) {
	// The first member refuses the request (kept as the answer); the second
	// fails on its credentials. The client gets the refusal, so the row must
	// carry the refusal's class, not the later member's.
	r := comboRig(t, []string{"forced/fm", "gem/g-vision"},
		tests.Reply{Status: 400, Body: `{"error":{"message":"bad"}}`},
		tests.Reply{Status: 401, Body: `{"error":{"message":"no"}}`})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	out, _ := r.do("/v1/chat/completions", comboAsk)
	if out.Status != 400 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.ErrorClass != "invalid_request" {
		t.Fatalf("the row's class is %q, want the refusal's", row.ErrorClass)
	}
}

func TestThePanelQuorumIsAtLeastTwoAndNeverMoreThanThePanel(t *testing.T) {
	for _, tt := range []struct{ min, members, want int }{
		{0, 5, 2}, {1, 5, 2}, {2, 5, 2}, {3, 5, 3}, {9, 5, 5}, {3, 2, 2},
	} {
		if got := panelQuorum(combo.Policy{MinPanel: tt.min}, tt.members); got != tt.want {
			t.Errorf("min %d of %d members: quorum %d, want %d", tt.min, tt.members, got, tt.want)
		}
	}
}

func TestARetryWaitsOutAShortCooldownAndEndsOnALongOneOrTheLastMember(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c := &call{p: &Pipeline{d: Deps{Now: func() time.Time { return now }}}}
	out := Output{Status: http.StatusServiceUnavailable}
	fail := func(after time.Duration) *accountFail {
		f := &accountFail{status: http.StatusServiceUnavailable}
		if after != 0 {
			f.resetAt = now.Add(after)
		}
		return f
	}
	for name, tt := range map[string]struct {
		after time.Duration
		last  bool
		stop  bool
	}{
		"no cooldown, more members":     {0, false, false},
		"no cooldown, the last member":  {0, true, true},
		"a short cooldown goes on":      {time.Millisecond, false, false},
		"a short cooldown, last member": {time.Millisecond, true, true},
		"a long cooldown ends it":       {settleCooldown + time.Second, false, true},
	} {
		t.Run(name, func(t *testing.T) {
			got, stop := c.settleRetry(context.Background(), fail(tt.after), out, tt.last)
			if stop != tt.stop {
				t.Fatalf("stop = %v, want %v", stop, tt.stop)
			}
			if stop && got.Status != out.Status {
				t.Fatalf("the answer changed: %d", got.Status)
			}
		})
	}
}

func TestARetryWaitThatTheClientEndsAnswersClosed(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	c := &call{p: &Pipeline{d: Deps{Now: func() time.Time { return now }}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, stop := c.settleRetry(ctx, &accountFail{status: 503, resetAt: now.Add(time.Second)}, Output{Status: 503}, false)
	if !stop || got.Status != statusClientClosed {
		t.Fatalf("%d stop=%v", got.Status, stop)
	}
}

// A single model that cannot read what the turn carries gets the configured
// fallback models tried first, and keeps its own place behind them.
func TestASoloModelTakesTheCapacityAdapter(t *testing.T) {
	const on = `{"capacityAdapter":{"vision":{"enabled":true,"models":["gem/g-vision"]}}}`
	// A model the tables call text-only: a model they never mention is tried with
	// the image first, so it would not need the adapter.
	body := imageBody("forced/deepseek-chat", "data:image/png;base64,iVBORw0KGgo=")

	r := newRig(t, tests.Reply{ContentType: "application/json", Body: geminiOK})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Settings = settingsRow{raw: on}
	out, _ := r.do("/v1/chat/completions", body)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "gem" || row.Model != "g-vision" {
		t.Fatalf("the adapter did not answer: %+v", row)
	}

	r = newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn("forced", "k1")
	r.conn("gem", "k2")
	r.p.d.Settings = settingsRow{raw: `{}`}
	out, _ = r.do("/v1/chat/completions", body)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "forced" {
		t.Fatalf("the adapter ran with the switch off: %+v", row)
	}
}

// A combo whose members are all text-only sends an image turn to the configured
// vision adapter, ahead of its own members.
func TestAComboWithNoVisionMemberTakesTheAdapter(t *testing.T) {
	r := comboRig(t, []string{"forced/deepseek-chat", "wire/deepseek-chat"}, tests.Reply{ContentType: "application/json", Body: geminiOK})
	r.conn("gem", "k2")
	r.conn("forced", "k1")
	r.conn("wire", "k3")
	r.p.d.Settings = settingsRow{raw: `{"capacityAdapter":{"vision":{"enabled":true,"models":["gem/g-vision"]}}}`}
	body := imageBody("chain", "data:image/png;base64,iVBORw0KGgo=")
	out, _ := r.do("/v1/chat/completions", body)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if row := r.last(); row.Provider != "gem" || row.ComboName != "chain" {
		t.Fatalf("%+v", row)
	}
}
