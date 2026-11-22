package pipeline

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/translate"
	"rllmx/tests"
)

func ruleFiles(hook, rules string) map[string]string {
	return map[string]string{
		"router/manifest.json": `{"id":"router","name":"Router","description":"declarative rules",` +
			`"hook":"` + hook + `","default_enabled":true}`,
		"router/rules.json": rules,
	}
}

func TestRulesRerouteOnceAndInjectOnce(t *testing.T) {
	c := skillCall(t, `{}`, nil, ruleFiles(skill.StagePreRequest,
		`{"rules":[{"stage":"pre-request","when":{"messageRegex":"refactor"},"then":{"injectPrompt":"RULE PROMPT"}}]}`))
	c.evaluateRules(context.Background())
	if c.rules.Empty() || len(c.rules.Prompts) != 1 {
		t.Fatalf("rules %+v", c.rules)
	}
	c.applyRulePrompts()
	c.applyRulePrompts()
	if n := strings.Count(hubSystem(c), "RULE PROMPT"); n != 1 {
		t.Fatalf("the prompt is present %d times", n)
	}
}

func TestRulesFollowTheSkillSwitch(t *testing.T) {
	files := ruleFiles(skill.StagePreRoute,
		`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^m$"},"then":{"rerouteModel":"wire/short"}}]}`)
	c := skillCall(t, `{"routerEnabled":false}`, nil, files)
	c.evaluateRules(context.Background())
	if !c.rules.Empty() {
		t.Fatalf("a disabled rule matched: %+v", c.rules)
	}
	on := skillCall(t, `{"routerEnabled":true}`, nil, files)
	on.evaluateRules(context.Background())
	if on.rules.Reroute != "wire/short" {
		t.Fatalf("reroute %q", on.rules.Reroute)
	}
}

func TestNoRulesSkipsTheHooks(t *testing.T) {
	c := skillCall(t, `{}`, nil, ruleFiles(skill.StagePreRoute,
		`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^m$"},"then":{"rerouteModel":"wire/short"}}]}`))
	c.in.NoRules = true
	c.evaluateRules(context.Background())
	if !c.rules.Empty() {
		t.Fatalf("an internal call ran the rules: %+v", c.rules)
	}
}

func TestBothSkillBypassHeadersLeaveTheRulesOff(t *testing.T) {
	h := http.Header{}
	h.Set(skill.BypassLocal, "off")
	h.Set(skill.BypassECC, "off")
	c := skillCall(t, `{}`, h, ruleFiles(skill.StagePreRoute,
		`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^m$"},"then":{"rerouteModel":"wire/short"}}]}`))
	c.evaluateRules(context.Background())
	if !c.rules.Empty() {
		t.Fatalf("a bypassed request ran the rules: %+v", c.rules)
	}
}

func TestRuleHeadersAreFilteredAgain(t *testing.T) {
	operator := map[string]string{"X-Team": "operator", "X-Title": "rllmx"}
	headers := [][2]string{
		{"Authorization", "leak"},
		{"Host", "evil"},
		{"Cookie", "a=b"},
		{"Content-Length", "9"},
		{"X-Secret-Key", "leak"},
		{"X-Bad", "a\nb"},
		{"X-Team", "rule"},
		{"X-Trace", "rule"},
	}
	got := applyRuleHeaders(operator, headers, "X-Secret-Key")
	if _, ok := got["Authorization"]; ok {
		t.Fatal("authorization was set")
	}
	if _, ok := got["Host"]; ok {
		t.Fatal("host was set")
	}
	if _, ok := got["Cookie"]; ok {
		t.Fatal("cookie was set")
	}
	if _, ok := got["X-Bad"]; ok {
		t.Fatal("a control character reached a header")
	}
	if got["X-Team"] != "operator" {
		t.Fatalf("a rule overwrote the operator's header: %q", got["X-Team"])
	}
	if got["X-Trace"] != "rule" || got["X-Title"] != "rllmx" {
		t.Fatalf("headers %v", got)
	}
	if _, ok := operator["X-Trace"]; ok {
		t.Fatal("the operator's map was rewritten")
	}
	if got := applyRuleHeaders(nil, nil, ""); got != nil {
		t.Fatal("no headers made a map")
	}
}

func ruleRig(t *testing.T, hook, rules string) *rig {
	t.Helper()
	r := newRig(t, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.Skills = ruleRegistry(t, hook, rules)
	r.p.d.Settings = settingsRow{raw: "{}"}
	return r
}

// ruleRegistry installs one rule-bearing skill in its own folder.
func ruleRegistry(t *testing.T, hook, rules string) *skill.Registry {
	t.Helper()
	root := t.TempDir()
	for name, body := range ruleFiles(hook, rules) {
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

func TestAPreRouteRuleRewritesTheModel(t *testing.T) {
	r := ruleRig(t, skill.StagePreRoute,
		`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^forced/fm$"},"then":{"rerouteModel":"wire/short"}}]}`)
	r.conn("forced", "k1")
	r.conn("wire", "k2")
	out := r.runAs(identity.Caller{ID: "k"}, "/v1/chat/completions",
		`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	calls := r.up.Calls()
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), "vendor/long-name") {
		t.Fatalf("calls %+v", calls)
	}
}

func TestARerouteNeverWidensARestrictedKey(t *testing.T) {
	r := ruleRig(t, skill.StagePreRoute,
		`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^forced/fm$"},"then":{"rerouteModel":"wire/short"}}]}`)
	r.conn("forced", "k1")
	r.conn("wire", "k2")
	body := `{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`

	// The rerouted target is outside the list.
	onlyOriginal := identity.Caller{ID: "k", Restricted: true, Allow: []string{"forced/fm"}}
	if out := r.runAs(onlyOriginal, "/v1/chat/completions", body); out.Status != http.StatusForbidden {
		t.Fatalf("target not listed: %d %s", out.Status, out.Body)
	}
	// The name the client asked for is outside the list, even though the rule
	// would take it to one that is allowed.
	onlyTarget := identity.Caller{ID: "k", Restricted: true, Allow: []string{"wire/short"}}
	if out := r.runAs(onlyTarget, "/v1/chat/completions", body); out.Status != http.StatusForbidden {
		t.Fatalf("original not listed: %d %s", out.Status, out.Body)
	}
	if len(r.up.Calls()) != 0 {
		t.Fatalf("a refused reroute reached the provider %d times", len(r.up.Calls()))
	}
	both := identity.Caller{ID: "k", Restricted: true, Allow: []string{"forced/fm", "wire/short"}}
	if out := r.runAs(both, "/v1/chat/completions", body); out.Status != 200 {
		t.Fatalf("both listed: %d %s", out.Status, out.Body)
	}
}

func TestAPreRequestRuleSetsAHeader(t *testing.T) {
	r := ruleRig(t, skill.StagePreRequest,
		`{"rules":[{"stage":"pre-request","when":{"messageRegex":"hi"},"then":{"injectPrompt":"RULE PROMPT",`+
			`"setHeader":{"name":"x-team","value":"platform"}}}]}`)
	r.conn("forced", "k1")
	out := r.runAs(identity.Caller{ID: "k"}, "/v1/chat/completions",
		`{"model":"forced/fm","messages":[{"role":"user","content":"hi"}]}`)
	if out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	calls := r.up.Calls()
	if len(calls) != 1 || calls[0].Header.Get("X-Team") != "platform" {
		t.Fatalf("header %v", calls)
	}
	if !strings.Contains(string(calls[0].Body), "RULE PROMPT") {
		t.Fatalf("body %s", calls[0].Body)
	}
}

func TestTheCacheKeyNamesTheMatchedRules(t *testing.T) {
	files := ruleFiles(skill.StagePreRequest,
		`{"rules":[{"stage":"pre-request","when":{"messageRegex":"refactor"},"then":{"injectPrompt":"R"}}]}`)
	hit := skillCall(t, `{}`, nil, files)
	hit.evaluateRules(context.Background())
	miss := skillCall(t, `{}`, nil, files)
	miss.req.Messages[0] = translate.Message{Role: translate.RoleUser, Content: translate.TextPart("nothing")}
	miss.evaluateRules(context.Background())
	if hit.skillKey(context.Background()) == miss.skillKey(context.Background()) {
		t.Fatal("a matched rule did not change the cache key")
	}
}
