package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/conversation"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/routing"
	"rllmx/app/llm/stream"
	"rllmx/app/store/repository"
	"rllmx/tests"
)

const buildAsk = "build a full auth service across multiple files with database and tokens"

const planText = "<PLAN>\n1. Write auth.go\n</PLAN>"

// chatJSON is one OpenAI chat body for a non-stream request.
func chatJSON(model, text string) string {
	b, _ := json.Marshal(map[string]any{
		"model":       model,
		"temperature": 0,
		"messages":    []map[string]string{{"role": "user", "content": text}},
	})
	return string(b)
}

// answerJSON is a complete OpenAI answer with the given content.
func answerJSON(content string) string {
	b, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-plan", "object": "chat.completion", "created": 1, "model": "m",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop",
			"message": map[string]string{"role": "assistant", "content": content}}},
		"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 7, "total_tokens": 12},
	})
	return string(b)
}

func planReply() tests.Reply {
	return tests.Reply{ContentType: "application/json", Body: answerJSON(planText)}
}

func codeReply() tests.Reply {
	return tests.Reply{ContentType: "application/json", Body: okJSON}
}

// planSettings joins extra keys onto the enabled automatic-plan row.
func planSettings(extra ...string) string {
	base := []string{
		`"autoPlanEnabled":true`,
		`"autoPlanMode":"manual"`,
		`"autoPlanComboId":"planner"`,
		`"autoPlanComplexityThreshold":6`,
	}
	return "{" + strings.Join(append(base, extra...), ",") + "}"
}

// planRig is the chat rig with a settings row and two stored combos: one plans,
// one codes.
func planRig(t *testing.T, settings string, steps ...tests.Reply) *rig {
	t.Helper()
	r := newRig(t, steps...)
	r.p.d.Settings = settingsRow{raw: settings}
	r.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	r.p.d.Resolver = &routing.Resolver{Registry: r.p.d.Registry, Snapshot: routing.NewSnapshot(source{
		combos: []string{"planner", "coder"},
		comboMembers: []repository.Combo{
			{ID: "p1", Name: "planner", Models: []string{"wire/short"}},
			{ID: "c1", Name: "coder", Models: []string{"forced/fm"}},
		},
	}, nil)}
	return r
}

// lastBody is the body of the n-th upstream call.
func lastBody(t *testing.T, r *rig, n int) string {
	t.Helper()
	calls := r.up.Calls()
	if len(calls) <= n {
		t.Fatalf("%d upstream calls, wanted number %d", len(calls), n+1)
	}
	return string(calls[n].Body)
}

// hasPlanTurn reports whether a request body carries the injected plan turn
// immediately before the final user turn.
func hasPlanTurn(t *testing.T, body string) bool {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		return false
	}
	if len(req.Messages) < 2 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	plan := req.Messages[len(req.Messages)-2]
	return last.Role == "user" && plan.Role == "system" &&
		strings.Contains(plan.Content, "Write auth.go") &&
		strings.Contains(plan.Content, "Implement the code following this plan faithfully.")
}

func TestThePlanPrecedesTheLastUserTurn(t *testing.T) {
	r := planRig(t, planSettings(), planReply(), codeReply())
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	out := r.runAs(identity.Caller{ID: "k1"}, "/v1/chat/completions", chatJSON("forced/fm", buildAsk))
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if n := len(r.up.Calls()); n != 2 {
		t.Fatalf("%d upstream calls", n)
	}
	if !hasPlanTurn(t, lastBody(t, r, 1)) {
		t.Fatalf("the code request carries no plan turn: %s\nfirst call: %s", lastBody(t, r, 1), lastBody(t, r, 0))
	}
	if hasPlanTurn(t, lastBody(t, r, 0)) {
		t.Fatalf("the plan request carried a plan: %s", lastBody(t, r, 0))
	}
}

// hermeticRegistry installs one always-on system-prompt skill and one
// pre-request rule, both of which would decorate an ordinary request.
func hermeticRegistry(t *testing.T) *skill.Registry {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"router/manifest.json": `{"id":"router","name":"Router","description":"rules","hook":"pre-request","default_enabled":true}`,
		"router/rules.json":    `{"rules":[{"stage":"pre-request","when":{"messageRegex":"auth"},"then":{"injectPrompt":"RULE PROMPT"}}]}`,
		"style/manifest.json":  `{"id":"style","name":"Style","description":"d","hook":"system-prompt","default_enabled":true,"routing_mode":"always"}`,
		"style/prompt.txt":     "STYLE PROMPT",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return skill.NewRegistry(root, nil)
}

func TestThePlanCallIsHermetic(t *testing.T) {
	r := planRig(t, planSettings(), planReply(), codeReply())
	r.p.d.Skills = hermeticRegistry(t)
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	out := r.runAs(identity.Caller{ID: "k1"}, "/v1/chat/completions",
		`{"model":"forced/fm","messages":[{"role":"user","content":"`+buildAsk+`"}],"tools":[{"type":"function","function":{"name":"f","parameters":{}}}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if n := len(r.up.Calls()); n != 2 {
		t.Fatalf("%d upstream calls, so the plan call recursed", n)
	}
	planBody := lastBody(t, r, 0)
	for _, unwanted := range []string{"RULE PROMPT", "STYLE PROMPT", `"tools"`} {
		if strings.Contains(planBody, unwanted) {
			t.Fatalf("the plan call carries %q: %s", unwanted, planBody)
		}
	}
	codeBody := lastBody(t, r, 1)
	for _, wanted := range []string{"RULE PROMPT", "STYLE PROMPT"} {
		if !strings.Contains(codeBody, wanted) {
			t.Fatalf("the code call is missing %q: %s", wanted, codeBody)
		}
	}
}

func TestAutoPlanRunsForEveryClientFormat(t *testing.T) {
	cases := map[string]struct {
		path string
		body string
		conn string
	}{
		"claude": {
			"/v1/messages",
			`{"model":"forced/fm","max_tokens":128,"messages":[{"role":"user","content":"` + buildAsk + `"}]}`,
			"forced",
		},
		"gemini": {
			"/v1beta/models/forced/fm:generateContent",
			`{"contents":[{"role":"user","parts":[{"text":"` + buildAsk + `"}]}]}`,
			"forced",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := planRig(t, planSettings(), planReply(), codeReply())
			r.conn(c.conn, "k1")
			r.conn("wire", "kw")
			out := r.runAs(identity.Caller{ID: "k1"}, c.path, c.body)
			if out.Status != 200 {
				t.Fatalf("%d %s", out.Status, out.Body)
			}
			calls := r.up.Calls()
			if len(calls) != 2 {
				t.Fatalf("%d upstream calls", len(calls))
			}
			// Both internal calls speak OpenAI, so the plan turn is visible in the
			// openai-shaped body before the final user turn.
			if !hasPlanTurn(t, string(calls[1].Body)) {
				t.Fatalf("no plan turn: %s", calls[1].Body)
			}
		})
	}
}

func TestAutoPlanFailsOpen(t *testing.T) {
	cases := map[string]struct {
		settings string
		header   http.Header
		body     string
		replies  []tests.Reply
		calls    int
		noCombos bool
	}{
		"disabled": {
			settings: `{"autoPlanEnabled":false}`,
			body:     chatJSON("forced/fm", buildAsk),
			calls:    1,
		},
		"no settings row": {
			settings: `{}`,
			body:     chatJSON("forced/fm", buildAsk),
			calls:    1,
		},
		"bypass header": {
			settings: planSettings(),
			header:   http.Header{"X-9router-Auto-Plan": []string{"off"}},
			body:     chatJSON("forced/fm", buildAsk),
			calls:    1,
		},
		"short ask": {
			settings: planSettings(),
			body:     chatJSON("forced/fm", "fix the typo"),
			calls:    1,
		},
		"already planned": {
			settings: planSettings(),
			body:     chatJSON("forced/fm", buildAsk+" <PLAN>old</PLAN>"),
			calls:    1,
		},
		"no combos": {
			settings: planSettings(),
			body:     chatJSON("forced/fm", buildAsk),
			calls:    1,
			noCombos: true,
		},
		"upstream error": {
			settings: planSettings(),
			body:     chatJSON("forced/fm", buildAsk),
			replies:  []tests.Reply{{Status: 500, Body: "nope"}, codeReply()},
			calls:    2,
		},
		"empty answer": {
			settings: planSettings(),
			body:     chatJSON("forced/fm", buildAsk),
			replies:  []tests.Reply{{ContentType: "application/json", Body: answerJSON("  ")}, codeReply()},
			calls:    2,
		},
		"timeout": {
			settings: planSettings(`"autoPlanTimeoutMs":1000`),
			body:     chatJSON("forced/fm", buildAsk),
			replies:  []tests.Reply{{Hang: true}, codeReply()},
			calls:    2,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			steps := c.replies
			if len(steps) == 0 {
				steps = []tests.Reply{codeReply()}
			}
			r := planRig(t, c.settings, steps...)
			if c.noCombos {
				r.p.d.Resolver = &routing.Resolver{Registry: r.p.d.Registry, Snapshot: routing.NewSnapshot(source{}, nil)}
			}
			r.conn("forced", "k1")
			r.conn("wire", "kw")
			in := Input{Path: "/v1/chat/completions", Body: []byte(c.body), Caller: identity.Caller{ID: "k1"}, RequestID: "rid-1", Header: c.header}
			out := r.p.Handle(context.Background(), in, func() (stream.Client, error) { return nil, nil })
			if out.Status != 200 {
				t.Fatalf("%d %s", out.Status, out.Body)
			}
			calls := r.up.Calls()
			if len(calls) != c.calls {
				t.Fatalf("%d upstream calls, want %d", len(calls), c.calls)
			}
			if hasPlanTurn(t, string(calls[len(calls)-1].Body)) {
				t.Fatalf("a failed plan was still injected: %s", calls[len(calls)-1].Body)
			}
		})
	}
}

func TestTheCodeComboAnswersTheRequest(t *testing.T) {
	r := planRig(t, planSettings(`"autoCodeComboId":"coder"`), planReply(), codeReply())
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	// The client names the planning combo's member, so the code call can only
	// reach "fm" by the switch.
	out := r.runAs(identity.Caller{ID: "k1"}, "/v1/chat/completions", chatJSON("wire/short", buildAsk))
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	body := lastBody(t, r, 1)
	if !strings.Contains(body, `"model":"fm"`) || !hasPlanTurn(t, body) {
		t.Fatalf("the code combo did not answer: %s", body)
	}
	if planCall := lastBody(t, r, 0); !strings.Contains(planCall, "vendor/long-name") {
		t.Fatalf("the plan did not go to the planning combo: %s", planCall)
	}
}

func TestACodeComboCannotWidenARestrictedKey(t *testing.T) {
	r := planRig(t, planSettings(`"autoCodeComboId":"coder"`), codeReply(), codeReply())
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	key := identity.Caller{ID: "k", Restricted: true, Allow: []string{"wire/short", "planner"}}
	out := r.runAs(key, "/v1/chat/completions", chatJSON("wire/short", buildAsk))
	if out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	// The key may call the planning combo but not the code combo, so no plan is
	// paid for and the request is answered as the client named it.
	calls := r.up.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d upstream calls, want the one answer", len(calls))
	}
	body := string(calls[0].Body)
	if hasPlanTurn(t, body) || !strings.Contains(body, "vendor/long-name") {
		t.Fatalf("the request was planned or moved: %s", body)
	}
}

func TestARestrictedKeyThatMayNotPlanIsNotPlanned(t *testing.T) {
	r := planRig(t, planSettings(`"autoCodeComboId":"coder"`), planReply(), codeReply())
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	key := identity.Caller{ID: "k", Restricted: true, Allow: []string{"forced/fm"}}
	out := r.runAs(key, "/v1/chat/completions", chatJSON("forced/fm", buildAsk))
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if n := len(r.up.Calls()); n != 1 || hasPlanTurn(t, lastBody(t, r, 0)) {
		t.Fatalf("%d upstream calls, and a plan was made for a key that may not call its combo", n)
	}
}

func TestAPlanIsNotServedFromTheCache(t *testing.T) {
	cr := newCacheRig(t, planReply(), codeReply(), planReply(), codeReply())
	cr.p.d.Settings = settingsRow{raw: strings.TrimSuffix(cacheOn, "}") + "," + strings.TrimPrefix(planSettings(), "{")}
	cr.p.d.Resolver = &routing.Resolver{Registry: cr.p.d.Registry, Snapshot: routing.NewSnapshot(source{
		combos:       []string{"planner"},
		comboMembers: []repository.Combo{{ID: "p1", Name: "planner", Models: []string{nodeID + "/gpt-x"}}},
	}, nil)}
	body := chatJSON("m", buildAsk)
	// The plan call leaves its own usage row, so the one-row assertion of do
	// does not apply here.
	for range 2 {
		out, _ := cr.run("/v1/chat/completions", body)
		if out.Status != 200 {
			t.Fatalf("%d %s", out.Status, out.Body)
		}
		if out.Header.Get(CacheHeader) != "" {
			t.Fatalf("a planned request was answered from the cache: %q", out.Header.Get(CacheHeader))
		}
	}
	if n := len(cr.up.Calls()); n != 4 {
		t.Fatalf("%d upstream calls, so a planned answer was stored", n)
	}
}

// routerAnswer is a complete answer naming the second member, so the smart
// combo's choice is made without a retry.
func routerAnswer() tests.Reply {
	return tests.Reply{ContentType: "application/json", Body: answerJSON(`{"model":"forced/fm","confidence":0.9}`)}
}

func TestAnInternalCallCarriesNoSkillSaverOrSession(t *testing.T) {
	r := comboRig(t, []string{"wire/short", "forced/fm"}, routerAnswer(), codeReply())
	r.p.d.Skills = hermeticRegistry(t)
	r.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	r.p.d.Rotation = combo.NewRotation()
	r.p.d.Sticky = combo.NewSticky()
	r.p.d.Settings = tests.Settings{JSON: `{"comboStrategies":{"chain":{"fallbackStrategy":"smart","routerModel":"wire/short"}},` +
		`"customPromptEnabled":true,"customPrompt":"ANSWER IN FRENCH"}`}
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	rec := httptest.NewRecorder()
	in := Input{
		Path: "/v1/chat/completions", Body: []byte(comboAsk), Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-1",
		Header: http.Header{"X-Session-Id": []string{"s-1"}},
	}
	out := r.p.Handle(context.Background(), in, func() (stream.Client, error) { return stream.NewSSE(rec, stream.Options{}) })
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	calls := r.up.Calls()
	if len(calls) < 2 {
		t.Fatalf("%d upstream calls, the router was not asked", len(calls))
	}
	question := string(calls[0].Body)
	for _, unwanted := range []string{"STYLE PROMPT", "ANSWER IN FRENCH"} {
		if strings.Contains(question, unwanted) {
			t.Fatalf("the router question carries %q: %s", unwanted, question)
		}
	}
	real := string(calls[len(calls)-1].Body)
	if !strings.Contains(real, "STYLE PROMPT") {
		t.Fatalf("the real request lost the skill prompt: %s", real)
	}
}

func TestAKeyThatMayNotCallThePlanningComboIsNotPlannedEvenWithoutACodeCombo(t *testing.T) {
	r := planRig(t, planSettings(), planReply(), codeReply())
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	key := identity.Caller{ID: "k", Restricted: true, Allow: []string{"forced/fm"}}
	out := r.runAs(key, "/v1/chat/completions", chatJSON("forced/fm", buildAsk))
	if out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if n := len(r.up.Calls()); n != 1 || hasPlanTurn(t, lastBody(t, r, 0)) {
		t.Fatalf("%d upstream calls; a plan was made for a key that may not call the planning combo", n)
	}
	// The key is not even tried against the planning combo, which would leave a
	// refused row of its own.
	if rows := len(r.rows.All()); rows != 1 {
		t.Fatalf("%d usage rows, want the one for the request", rows)
	}
}

const ambiguousAsk = "please refactor and implement the module loader with clearer errors over the whole tree"

func TestTheClassifierQuestionGoesToThePlanningComboByName(t *testing.T) {
	// A key allowed the combo and nothing else must be able to ask the question,
	// which a member model of the combo would refuse.
	r := planRig(t, planSettings(`"autoPlanSmartClassify":true`),
		tests.Reply{ContentType: "application/json", Body: answerJSON("YES")}, planReply(), codeReply())
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	key := identity.Caller{ID: "k", Restricted: true, Allow: []string{"planner", "forced/fm"}}
	out := r.runAs(key, "/v1/chat/completions", chatJSON("forced/fm", ambiguousAsk))
	if out.Status != http.StatusOK {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if n := len(r.up.Calls()); n != 3 {
		t.Fatalf("%d upstream calls, want the question, the plan and the answer", n)
	}
	if !strings.Contains(lastBody(t, r, 0), "Jawab hanya YES atau NO") {
		t.Fatalf("the first call was not the classifier question: %s", lastBody(t, r, 0))
	}
	for _, row := range r.rows.All() {
		if row.Status == http.StatusForbidden {
			t.Fatalf("an internal call was refused for the key: %+v", row)
		}
	}
}
