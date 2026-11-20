package pipeline

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/gateway/skill"
	"rllmx/app/llm/translate"
	"rllmx/app/store/document"
	"rllmx/tests"
)

func skillFiles() map[string]string {
	return map[string]string{
		"terse/manifest.json": `{"id":"terse","name":"Terse","description":"short answers","hook":"system-prompt",` +
			`"default_enabled":true,"routing_mode":"always"}`,
		"terse/prompt.txt": "TERSE PROMPT",
		"simple/manifest.json": `{"id":"simple","name":"Simplifier","description":"smallest change that works",` +
			`"hook":"system-prompt","routable":true,"default_enabled":true,"routing_mode":"smart",` +
			`"triggers":["keep it simple"],"keywords":["refactor","simplify"]}`,
		"simple/prompt.txt": "SIMPLE PROMPT",
		"catalog/manifest.json": `{"id":"catalog","name":"Catalog","description":"imported catalog skill",` +
			`"hook":"system-prompt","source":"ecc","default_enabled":true,"routing_mode":"smart",` +
			`"triggers":["dependency upgrade"],"keywords":["upgrade"]}`,
		"catalog/prompt.txt": "CATALOG PROMPT",
	}
}

func skillCall(t *testing.T, row string, header http.Header, files map[string]string) *call {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := &Pipeline{}
	p.d.Skills = skill.NewRegistry(root, nil)
	if row != "" {
		p.d.Settings = settingsRow{raw: row}
	}
	return &call{
		p:    p,
		in:   Input{Header: header},
		tctx: translate.NewCtx("openai", "openai"),
		req:  &translate.Request{Model: "m", Messages: []translate.Message{{Role: translate.RoleUser, Content: translate.TextPart("please refactor, keep it simple")}}},
	}
}

func hubSystem(c *call) string {
	for _, m := range c.req.Messages {
		if m.Role == translate.RoleSystem {
			return m.Text()
		}
	}
	return ""
}

func TestApplySkillsInjectsThroughTheSettings(t *testing.T) {
	c := skillCall(t, `{}`, nil, skillFiles())
	c.p.d.Log = &tests.Log{}
	c.applySkills(context.Background())
	body := hubSystem(c)
	if !strings.Contains(body, "TERSE PROMPT") || !strings.Contains(body, "SIMPLE PROMPT") {
		t.Fatalf("system %q", body)
	}
	c.applySkills(context.Background())
	if n := strings.Count(hubSystem(c), "TERSE PROMPT"); n != 1 {
		t.Fatalf("the prompt is present %d times", n)
	}
}

func bypassHeader(name string) http.Header {
	h := http.Header{}
	h.Set(name, "off")
	return h
}

func TestTheSkillBypassHeadersTurnEachRouterOff(t *testing.T) {
	local := skillCall(t, `{}`, bypassHeader(skill.BypassLocal), skillFiles())
	local.applySkills(context.Background())
	if body := hubSystem(local); strings.Contains(body, "TERSE PROMPT") {
		t.Fatalf("the local router did not stop: %q", body)
	}

	eccRow := `{"ecc_auto_skill_routerEnabled":true}`
	ecc := skillCall(t, eccRow, bypassHeader(skill.BypassECC), skillFiles())
	ecc.req.Messages[0] = translate.Message{Role: translate.RoleUser, Content: translate.TextPart("dependency upgrade")}
	ecc.applySkills(context.Background())
	if strings.Contains(hubSystem(ecc), "CATALOG PROMPT") {
		t.Fatal("the catalog router did not stop")
	}

	on := skillCall(t, eccRow, nil, skillFiles())
	on.req.Messages[0] = translate.Message{Role: translate.RoleUser, Content: translate.TextPart("dependency upgrade")}
	on.applySkills(context.Background())
	if !strings.Contains(hubSystem(on), "CATALOG PROMPT") {
		t.Fatal("the catalog router is off without the header")
	}
}

func TestNoRegistryMeansNoSkill(t *testing.T) {
	c := saverCall(`{"ecc_auto_skill_routerEnabled":true}`, nil)
	c.applySkills(context.Background())
	if hubSystem(c) != "" {
		t.Fatal("a nil registry injected")
	}
	if c.skillKey(context.Background()) != "" {
		t.Fatal("a nil registry has a key")
	}
}

func TestTheCacheKeyNamesTheInstalledAndEnabledSet(t *testing.T) {
	row := `{"terseEnabled":true,"simpleEnabled":true}`
	c := skillCall(t, row, nil, skillFiles())
	base := c.skillKey(context.Background())
	if base == "" {
		t.Fatal("no key with skills enabled")
	}
	if c.skillKey(context.Background()) != base {
		t.Fatal("the same configuration changed the key")
	}
	off := skillCall(t, `{"terseEnabled":false,"simpleEnabled":true}`, nil, skillFiles()).skillKey(context.Background())
	if off == base {
		t.Fatal("a disabled skill does not change the key")
	}
	other := skillFiles()
	other["simple/prompt.txt"] = "A DIFFERENT PROMPT"
	changed := skillCall(t, row, nil, other).skillKey(context.Background())
	if changed == base {
		t.Fatal("changed prompt text does not change the key")
	}
	both := bypassHeader(skill.BypassLocal)
	both.Set(skill.BypassECC, "off")
	bypass := skillCall(t, row, both, skillFiles()).skillKey(context.Background())
	if bypass != "" {
		t.Fatal("a bypassed request carries a key")
	}
}

func TestTheResponseKeyCarriesTheSkillSet(t *testing.T) {
	body := []byte(`{"a":1}`)
	on := skillCall(t, `{"terseEnabled":true}`, nil, skillFiles()).requestKey(context.Background(), body)
	off := skillCall(t, `{"terseEnabled":false}`, nil, skillFiles()).requestKey(context.Background(), body)
	if on == off {
		t.Fatal("the response key ignores the skill set")
	}
}

func TestReadSettingReadsTheTwoKeys(t *testing.T) {
	doc, err := document.ParseDoc(`{"terseEnabled":false,"terseRoutingMode":"always","simpleRoutingMode":"sometimes"}`)
	if err != nil {
		t.Fatal(err)
	}
	s := readSetting(doc, "terse")
	if !s.HasEnabled || s.Enabled || !s.HasMode || s.Mode != "always" {
		t.Fatalf("setting %+v", s)
	}
	s = readSetting(doc, "simple")
	if s.HasEnabled || s.HasMode {
		t.Fatalf("a typo was accepted: %+v", s)
	}
}
