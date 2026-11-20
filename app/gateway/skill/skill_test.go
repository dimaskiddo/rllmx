package skill

import (
	"strings"
	"testing"

	"rllmx/app/llm/translate"
)

func installed(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, files)
	return NewRegistry(root, nil)
}

func standard(t *testing.T) *Registry {
	t.Helper()
	return installed(t, map[string]string{
		"terse/manifest.json": `{"id":"terse","name":"Terse","description":"short answers","hook":"system-prompt",` +
			`"default_enabled":true,"routing_mode":"always","triggers":["be concise"],"keywords":["terse"]}`,
		"terse/prompt.txt": "TERSE PROMPT",
		"simple/manifest.json": `{"id":"simple","name":"Simplifier","description":"smallest change that works",` +
			`"hook":"system-prompt","routable":true,"default_enabled":true,"routing_mode":"smart","routing_threshold":0.3,` +
			`"triggers":["keep it simple"],"keywords":["refactor","simplify"]}`,
		"simple/prompt.txt": "SIMPLE PROMPT",
		"commit/manifest.json": `{"id":"commit","name":"Commit Lint","description":"check a commit message",` +
			`"hook":"system-prompt","legacy_enabled_key":"commitLintEnabled","routable":true,` +
			`"triggers":["commit message"],"keywords":["commit","lint"]}`,
		"commit/prompt.txt": "COMMIT PROMPT",
		"rules/manifest.json": `{"id":"rules","name":"Rules","description":"a declarative rule set",` +
			`"hook":"pre-request","default_enabled":true,"routing_mode":"always"}`,
		"rules/prompt.txt": "RULES PROMPT",
	})
}

func ask(text string) *translate.Request {
	return &translate.Request{Messages: []translate.Message{{Role: translate.RoleUser, Content: translate.TextPart(text)}}}
}

func systemText(r *translate.Request) string {
	for _, m := range r.Messages {
		if m.Role == translate.RoleSystem {
			return m.Text()
		}
	}
	return ""
}

func TestApplyInjectsAlwaysAndOnTopic(t *testing.T) {
	r := ask("please refactor this, keep it simple")
	st := Apply(standard(t), r, Options{})
	if !strings.Contains(systemText(r), "TERSE PROMPT") || !strings.Contains(systemText(r), "SIMPLE PROMPT") {
		t.Fatalf("system %q", systemText(r))
	}
	if st.Line() != "skills=terse+simple" {
		t.Fatalf("line %q", st.Line())
	}
}

func TestApplyLeavesAnUnrelatedQuestionAlone(t *testing.T) {
	r := ask("what is the capital of France")
	st := Apply(standard(t), r, Options{})
	body := systemText(r)
	if strings.Contains(body, "SIMPLE PROMPT") || strings.Contains(body, "COMMIT PROMPT") {
		t.Fatalf("a smart skill fired on an unrelated question: %q", body)
	}
	if !strings.Contains(body, "TERSE PROMPT") {
		t.Fatal("the always skill did not fire")
	}
	if len(st.Skills) != 1 {
		t.Fatalf("skills %v", st.Skills)
	}
}

func TestSettingOverridesTheManifest(t *testing.T) {
	off := Options{Setting: func(id string) Setting {
		if id == "terse" {
			return Setting{HasEnabled: true, Enabled: false}
		}
		return Setting{}
	}}
	r := ask("what is the capital of France")
	Apply(standard(t), r, off)
	if strings.Contains(systemText(r), "TERSE PROMPT") {
		t.Fatal("a disabled skill fired")
	}

	on := Options{Setting: func(id string) Setting {
		if id == "simple" {
			return Setting{HasEnabled: true, Enabled: true, HasMode: true, Mode: "always"}
		}
		return Setting{}
	}}
	r = ask("what is the capital of France")
	Apply(standard(t), r, on)
	if !strings.Contains(systemText(r), "SIMPLE PROMPT") {
		t.Fatal("an always override did not fire")
	}

	typo := Options{Setting: func(id string) Setting {
		return Setting{HasMode: true, Mode: "sometimes"}
	}}
	r = ask("please refactor this, keep it simple")
	Apply(standard(t), r, typo)
	if !strings.Contains(systemText(r), "SIMPLE PROMPT") {
		t.Fatal("a bad mode replaced the manifest's smart")
	}
}

func TestTheLegacyKeyEnablesASkill(t *testing.T) {
	o := Options{Setting: func(id string) Setting {
		if id == "commitLintEnabled" {
			return Setting{HasEnabled: true, Enabled: true}
		}
		return Setting{}
	}}
	r := ask("lint my commit message")
	Apply(standard(t), r, o)
	if !strings.Contains(systemText(r), "COMMIT PROMPT") {
		t.Fatal("the legacy enabled key did not enable the skill")
	}
}

func TestAtMostTwoSmartSkillsFire(t *testing.T) {
	body := `{"id":"%s","name":"%s","description":"smallest change","hook":"system-prompt","routable":true,` +
		`"default_enabled":true,"routing_mode":"smart","triggers":["keep it simple"],` +
		`"keywords":["refactor","simplify"]}`
	files := map[string]string{}
	for _, id := range []string{"a", "b", "c"} {
		prefix := id + "/"
		files[prefix+"manifest.json"] = strings.NewReplacer("%s", id).Replace(body)
		files[prefix+"prompt.txt"] = strings.ToUpper(id) + " PROMPT"
	}
	r := ask("please refactor and simplify, keep it simple")
	st := Apply(installed(t, files), r, Options{})
	if len(st.Skills) != maxLocal {
		t.Fatalf("skills %v, want %d", st.Skills, maxLocal)
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	reg := standard(t)
	r := ask("please refactor this, keep it simple")
	if st := Apply(reg, r, Options{}); !st.Changed() {
		t.Fatal("the first run changed nothing")
	}
	before := systemText(r)
	st := Apply(reg, r, Options{})
	if st.Changed() {
		t.Fatalf("the second run reported %v", st.Skills)
	}
	if systemText(r) != before {
		t.Fatal("the second run changed the prompt")
	}
	if n := strings.Count(systemText(r), "TERSE PROMPT"); n != 1 {
		t.Fatalf("the prompt is present %d times", n)
	}
}

func TestAHookThatIsNotASystemPromptNeverInjects(t *testing.T) {
	r := ask("anything")
	Apply(standard(t), r, Options{})
	if strings.Contains(systemText(r), "RULES PROMPT") {
		t.Fatal("a pre-request hook injected a prompt")
	}
}

func TestStatsNamesIdsNeverText(t *testing.T) {
	st := Stats{Skills: []string{"terse", "simple"}}
	if st.Line() != "skills=terse+simple" {
		t.Fatalf("line %q", st.Line())
	}
	if st.Changed() != true || (Stats{}).Changed() != false {
		t.Fatal("Changed is wrong")
	}
}

func TestApplyWithoutARegistryOrRequest(t *testing.T) {
	r := ask("keep it simple")
	if st := Apply(nil, r, Options{}); st.Changed() {
		t.Fatal("a nil registry changed the request")
	}
	if len(r.Messages) != 1 || r.Messages[0].Role != translate.RoleUser {
		t.Fatal("a nil registry touched the request")
	}
	if st := Apply(standard(t), nil, Options{}); st.Changed() {
		t.Fatal("a nil request was changed")
	}
}

func TestTheECCRouterScoresItsOwnCatalog(t *testing.T) {
	reg := installed(t, map[string]string{
		"imported/manifest.json": `{"id":"catalog-one","name":"Catalog One","description":"a catalog skill",` +
			`"hook":"system-prompt","source":"ecc","default_enabled":true,"routing_mode":"smart",` +
			`"conf_triggers":"x","triggers":["dependency upgrade"],"keywords":["upgrade","dependencies"]}`,
		"imported/prompt.txt": "CATALOG PROMPT",
		"local/manifest.json": `{"id":"local-one","name":"Local One","description":"a local skill",` +
			`"hook":"system-prompt","routable":true,"default_enabled":true,"routing_mode":"smart",` +
			`"triggers":["dependency upgrade"],"keywords":["upgrade","dependencies"]}`,
		"local/prompt.txt": "LOCAL PROMPT",
	})
	if got := reg.ECCIndex().Score("please upgrade the dependencies", 0); len(got) != 1 || got[0].ID != "catalog-one" {
		t.Fatalf("ecc index %+v", got)
	}
	if got := reg.LocalIndex().Score("please upgrade the dependencies", 0); len(got) != 1 || got[0].ID != "local-one" {
		t.Fatalf("local index %+v", got)
	}

	r := ask("please upgrade the dependencies")
	Apply(reg, r, Options{ECC: ECCOptions{Enabled: true}})
	if !strings.Contains(systemText(r), "CATALOG PROMPT") {
		t.Fatal("the catalog skill did not fire")
	}

	r = ask("upgrade")
	Apply(reg, r, Options{ECC: ECCOptions{Enabled: true, Confidence: 0.9}})
	if strings.Contains(systemText(r), "CATALOG PROMPT") {
		t.Fatal("a skill below the confidence fired")
	}

	r = ask("please upgrade the dependencies")
	Apply(reg, r, Options{})
	if strings.Contains(systemText(r), "CATALOG PROMPT") {
		t.Fatal("the catalog router fired while off")
	}
}
