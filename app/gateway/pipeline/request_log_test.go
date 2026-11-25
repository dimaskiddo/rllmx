package pipeline

import (
	"rllmx/app/llm/routing"
	"rllmx/tests"
	"strings"
	"testing"

	"rllmx/app/gateway/saver"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/translate"
	"rllmx/app/store/analytics"
)

func logCall() *call {
	return &call{
		asked:        "fast",
		requested:    "openai/gpt-4o",
		format:       "claude",
		clientStream: true,
		dest:         destination{provider: "openai", model: "gpt-4o", format: "openai"},
		req: &translate.Request{
			Messages: []translate.Message{{Role: translate.RoleUser, Content: translate.TextPart("SECRET-PROMPT-TEXT")}, {Role: translate.RoleUser, Content: translate.TextPart("two")}},
			Tools:    []translate.Tool{{Name: "bash"}},
			Thinking: translate.Thinking{Mode: "level", Level: "high"},
		},
		row: analytics.Record{ComboName: "my-combo", ConnectionID: "conn-1"},
	}
}

func TestTheRouteLineNamesTheAskedModelTheTargetAndTheComboWithoutAnyText(t *testing.T) {
	line := logCall().routeLine()
	for _, want := range []string{"POST fast → openai/gpt-4o", "FMT claude→openai", "STREAM", "2 MSG", "1 TOOL", "THINK high", `COMBO "my-combo"`, "ACC conn-1"} {
		if !strings.Contains(line, want) {
			t.Errorf("the route line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "SECRET-PROMPT-TEXT") {
		t.Fatalf("the route line carries prompt text: %s", line)
	}
}

func TestTheRouteLineOfAPlainRequestHasNoComboOrThinking(t *testing.T) {
	c := logCall()
	c.row.ComboName, c.req.Thinking, c.req.Tools, c.clientStream = "", translate.Thinking{}, nil, false
	line := c.routeLine()
	for _, no := range []string{"COMBO", "THINK", "TOOL", "STREAM"} {
		if strings.Contains(line, no) {
			t.Errorf("the line says %q for a request that had none: %s", no, line)
		}
	}
	if !strings.Contains(line, "JSON") {
		t.Errorf("a request answered whole should say JSON: %s", line)
	}
}

func TestAClientChosenNameCannotBreakALogLine(t *testing.T) {
	c := logCall()
	c.asked = "evil\nFAKE LINE " + strings.Repeat("x", 500)
	line := c.routeLine()
	if strings.ContainsAny(line, "\r\n") || len(line) > 600 {
		t.Fatalf("a model name shaped the log: %d bytes, %q", len(line), line[:60])
	}
}

func TestTheFeatureLineNamesLevelsModesAndSkillsOnly(t *testing.T) {
	c := logCall()
	c.saverStats = saver.Stats{
		Saved: 120, Filters: []string{"git", "ls"},
		Prompts: []string{"caveman", "ponytail", "custom"}, Levels: []string{"caveman=full", "ponytail=lite"},
		Headroom: true, CustomChars: 42, Skipped: []string{"rtk", "caveman"},
	}
	c.skillStats = skill.Stats{Skills: []string{"simplifier"}, Via: map[string]string{"simplifier": "always"}, Reminded: []string{"reviewer"}}
	line := c.featureLine()
	for _, want := range []string{"RTK 2", "CAVEMAN full", "PONYTAIL lite", "CUSTOMPROMPT 42c", "HEADROOM", "SKILL simplifier(always)", "SKILL reviewer(reminded)", "RTK skipped", "CAVEMAN skipped"} {
		if !strings.Contains(line, want) {
			t.Errorf("the feature line lacks %q: %s", want, line)
		}
	}
	c.saverStats, c.skillStats = saver.Stats{}, skill.Stats{}
	if got := c.featureLine(); got != "" {
		t.Fatalf("a request nothing acted on earned a line: %q", got)
	}
}

func TestTheDoneLineReportsTimingTokensAndStatus(t *testing.T) {
	line := doneLine(analytics.Record{
		RequestID: "rid-9", Status: 200, LatencyMs: 840, TTFBMs: 120,
		PromptTokens: 100, CachedTokens: 10, CompletionTokens: 50,
	})
	for _, want := range []string{"id=rid-9", "DONE 840ms", "TTFT 120ms", "IN 100", "CACHE ↻10", "OUT 50", "200"} {
		if !strings.Contains(line, want) {
			t.Errorf("the done line lacks %q: %s", want, line)
		}
	}
	if plain := doneLine(analytics.Record{Status: 502, LatencyMs: 5}); strings.Contains(plain, "CACHE") || strings.Contains(plain, "TTFT") {
		t.Errorf("a failed request invented a cache or first-token figure: %s", plain)
	}
}

func TestARequestLeavesARouteLineAndADoneLineAndNoPromptText(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.conn(nodeID, "k1")
	log := r.p.d.Log.(*tests.Log)
	r.do("/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"TOP-SECRET-QUESTION"}]}`)

	var route, done string
	for _, l := range log.Lines() {
		switch {
		case strings.HasPrefix(l, "POST "):
			route = l
		case strings.Contains(l, " DONE "):
			done = l
		}
		if strings.Contains(l, "TOP-SECRET-QUESTION") || strings.Contains(l, "k1") && strings.Contains(l, "Bearer") {
			t.Fatalf("a log line carries request text or a credential: %s", l)
		}
	}
	if !strings.Contains(route, "POST m → ") || !strings.Contains(route, "FMT openai→openai") || !strings.Contains(route, "1 MSG") || !strings.Contains(route, "JSON") {
		t.Errorf("route line: %q", route)
	}
	if !strings.Contains(done, "id=rid-1 DONE ") || !strings.Contains(done, "IN 10") || !strings.Contains(done, "OUT 5") || !strings.HasSuffix(done, " 200") {
		t.Errorf("done line: %q", done)
	}
}

func TestAComboRequestNamesTheComboAndWhatTheClientAsked(t *testing.T) {
	r := comboRig(t, []string{"forced/fm"}, tests.Reply{ContentType: "text/event-stream", Chunks: sseChunks()})
	r.conn("forced", "k1")
	log := r.p.d.Log.(*tests.Log)
	r.do("/v1/chat/completions", comboAsk)

	for _, l := range log.Lines() {
		if strings.HasPrefix(l, "POST ") {
			if !strings.Contains(l, `COMBO "chain"`) || !strings.Contains(l, "→ forced/fm") || !strings.Contains(l, "POST chain →") {
				t.Fatalf("the route line of a combo request: %q", l)
			}
			return
		}
	}
	t.Fatalf("no route line was written: %v", log.Lines())
}

// The levels a model serves travel to the writer with the model, so a level the
// client named is clamped to one the model accepts instead of being sent and
// refused.
func TestTheTranslateContextCarriesTheLevelsTheModelServes(t *testing.T) {
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	c := &call{p: r.p, tctx: translate.NewCtx("openai", "gemini-cli"), req: &translate.Request{}}
	c.comboContext(destination{provider: "antigravity", model: "gemini-4-neon-high", format: "gemini-cli"}, routing.Target{})
	if got := strings.Join(c.tctx.Levels, ","); got != "low,medium,high,xhigh" {
		t.Fatalf("levels %q", got)
	}
}
