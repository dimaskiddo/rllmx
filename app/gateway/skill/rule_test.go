package skill

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func TestRulesAreValidatedWhole(t *testing.T) {
	cases := map[string]struct {
		hook  string
		rules string
		bad   string
	}{
		"reroute": {
			StagePreRoute,
			`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^claude-"},"then":{"rerouteModel":"gpt-4o"}}]}`,
			"",
		},
		"inject and header": {
			StagePreRequest,
			`{"rules":[{"stage":"pre-request","when":{"messageRegex":"refactor","header":{"name":"x-client","regex":"^cli$"}},` +
				`"then":{"injectPrompt":"Small diffs.","setHeader":{"name":"x-team","value":"platform"}}}]}`,
			"",
		},
		"unknown stage":        {StagePreRoute, `{"rules":[{"stage":"post","when":{"modelRegex":"x"},"then":{"rerouteModel":"y"}}]}`, "stage"},
		"no matcher":           {StagePreRoute, `{"rules":[{"stage":"pre-route","when":{},"then":{"rerouteModel":"y"}}]}`, "when"},
		"bad pattern":          {StagePreRoute, `{"rules":[{"stage":"pre-route","when":{"modelRegex":"("},"then":{"rerouteModel":"y"}}]}`, "modelRegex"},
		"long pattern":         {StagePreRoute, `{"rules":[{"stage":"pre-route","when":{"modelRegex":"` + strings.Repeat("a", maxPattern+1) + `"},"then":{"rerouteModel":"y"}}]}`, "over"},
		"pre-route injects":    {StagePreRoute, `{"rules":[{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"y","injectPrompt":"p"}}]}`, "only rerouteModel"},
		"pre-request reroutes": {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"rerouteModel":"y"}}]}`, "may not rerouteModel"},
		"empty then":           {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{}}]}`, "needs"},
		"denied header":        {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"setHeader":{"name":"authorization","value":"x"}}}]}`, "may not set"},
		"cookie header":        {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"setHeader":{"name":"Cookie","value":"a=b"}}}]}`, "may not set"},
		"bad header name":      {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"setHeader":{"name":"x team","value":"v"}}}]}`, "not a header name"},
		"newline value":        {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"setHeader":{"name":"x-team","value":"a\nb"}}}]}`, "printable"},
		"unreadable match":     {StagePreRequest, `{"rules":[{"stage":"pre-request","when":{"header":{"name":"x-api-key","regex":"x"}},"then":{"injectPrompt":"p"}}]}`, "may read"},
		"hook mismatch":        {"system-prompt", `{"rules":[{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"y"}}]}`, "hook"},
		"too many":             {StagePreRoute, `{"rules":[` + strings.Repeat(`{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"y"}},`, maxRules) + `{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"y"}}]}`, "cap"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"manifest.json": `{"id":"rule-test","name":"Rule Test","description":"rules","hook":"` + c.hook + `"}`,
				"rules.json":    c.rules,
			}
			if c.hook != StagePreRoute && c.hook != StagePreRequest {
				files["prompt.txt"] = "text"
			}
			writeFiles(t, dir, files)
			_, _, err := LoadDir(dir)
			if c.bad == "" {
				if err != nil {
					t.Fatalf("a valid rules file was refused: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.bad) {
				t.Fatalf("error %v does not name %q", err, c.bad)
			}
		})
	}
}

func TestARuleFolderNeedsNoPrompt(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"router/manifest.json": `{"id":"router","name":"Router","description":"rules","hook":"pre-route"}`,
		"router/rules.json":    `{"rules":[{"stage":"pre-route","when":{"modelRegex":"^fast-"},"then":{"rerouteModel":"gpt-4o"}}]}`,
	})
	r := NewRegistry(root, nil)
	if got := r.Rules(StagePreRoute, nil); len(got) != 1 || got[0].ID != "router" {
		t.Fatalf("rules %+v", got)
	}
	if problems := r.Problems(); len(problems) != 0 {
		t.Fatalf("problems %v", problems)
	}
}

func TestRulesFollowTheEnabledSwitch(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a-router/manifest.json": `{"id":"a-router","name":"A","description":"d","hook":"pre-route","default_enabled":true}`,
		"a-router/rules.json":    `{"rules":[{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"a"}}]}`,
		"b-router/manifest.json": `{"id":"b-router","name":"B","description":"d","hook":"pre-route","default_enabled":true}`,
		"b-router/rules.json":    `{"rules":[{"stage":"pre-route","when":{"modelRegex":"x"},"then":{"rerouteModel":"b"}}]}`,
	})
	r := NewRegistry(root, nil)
	if got := r.Rules(StagePreRoute, func(m Manifest) bool { return m.ID == "b-router" }); len(got) != 1 || got[0].ID != "b-router" {
		t.Fatalf("rules %+v", got)
	}
	all := r.Rules(StagePreRoute, nil)
	if len(all) != 2 || all[0].ID != "a-router" || all[1].ID != "b-router" {
		t.Fatalf("order %+v", all)
	}
	if got := r.Rules(StagePreRequest, nil); got != nil {
		t.Fatalf("a pre-request lookup returned %+v", got)
	}
	var nilReg *Registry
	if nilReg.Rules(StagePreRoute, nil) != nil {
		t.Fatal("a nil registry returned rules")
	}
}

func TestMatchRulesCollectsActions(t *testing.T) {
	rules, err := rulesFrom([]byte(`{"rules":[
		{"stage":"pre-request","when":{"messageRegex":"(?i)refactor"},"then":{"injectPrompt":"One.","setHeader":{"name":"x-team","value":"first"}}},
		{"stage":"pre-request","when":{"modelRegex":"^claude-","header":{"name":"x-client","regex":"^cli$"}},"then":{"injectPrompt":"Two.","setHeader":{"name":"x-team","value":"second"}}},
		{"stage":"pre-request","when":{"modelRegex":"^other-"},"then":{"injectPrompt":"Never."}}
	]}`), "demo", StagePreRequest)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("X-Client", "cli")
	got := MatchRules(rules, "claude-sonnet", "please refactor this", h)
	if len(got.IDs) != 1 || got.IDs[0] != "demo" || len(got.Prompts) != 2 || got.Prompts[0] != "One." {
		t.Fatalf("actions %+v", got)
	}
	if len(got.Headers) != 1 || got.Headers[0][1] != "first" {
		t.Fatalf("headers %+v", got.Headers)
	}
	if got := MatchRules(rules, "claude-sonnet", "nothing", http.Header{}); !got.Empty() {
		t.Fatalf("a non-matching message matched: %+v", got)
	}
	if got := MatchRules(nil, "m", "x", http.Header{}); !got.Empty() {
		t.Fatal("no rules produced actions")
	}
}

func TestMatchRulesCapsHeaders(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"rules":[`)
	for i := range 12 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"stage":"pre-request","when":{"modelRegex":"x"},"then":{"setHeader":{"name":"x-h`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`","value":"v"}}}`)
	}
	b.WriteString(`]}`)
	rules, err := rulesFrom([]byte(b.String()), "many", StagePreRequest)
	if err != nil {
		t.Fatal(err)
	}
	if got := MatchRules(rules, "x", "", http.Header{}); len(got.Headers) != maxActions {
		t.Fatalf("headers %d", len(got.Headers))
	}
}

func TestMatchRulesCutsTheMessage(t *testing.T) {
	rules, err := rulesFrom([]byte(`{"rules":[{"stage":"pre-request","when":{"messageRegex":"z$"},"then":{"injectPrompt":"p"}}]}`),
		"cut", StagePreRequest)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", MatchMessage) + "z"
	if got := MatchRules(rules, "m", long, http.Header{}); !got.Empty() {
		t.Fatal("the tail past the cap matched")
	}
	if got := MatchRules(rules, "m", strings.Repeat("a", MatchMessage-1)+"z", http.Header{}); len(got.IDs) != 1 {
		t.Fatal("a message inside the cap did not match")
	}
}

func FuzzRules(f *testing.F) {
	f.Add(`{"rules":[{"stage":"pre-route","when":{"modelRegex":"^x"},"then":{"rerouteModel":"y"}}]}`)
	f.Add(`{"rules":[{"stage":"pre-request","when":{"header":{"name":"x-client","regex":"a"}},"then":{"injectPrompt":"p"}}]}`)
	f.Add(`{}`)
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > maxRulesFile {
			return
		}
		rules, err := rulesFrom([]byte(raw), "f", StagePreRequest)
		if err != nil {
			return
		}
		_ = MatchRules(rules, "m", "m", http.Header{})
	})
}
