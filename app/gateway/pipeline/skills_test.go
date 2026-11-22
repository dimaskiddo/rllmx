package pipeline

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/access/identity"
	"rllmx/app/gateway/conversation"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/stream"
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

func TestTheCacheKeyCarriesTheConfigValues(t *testing.T) {
	files := skillFiles()
	files["terse/manifest.json"] = `{"id":"terse","name":"Terse","description":"short answers","hook":"system-prompt",` +
		`"default_enabled":true,"routing_mode":"always","config_schema":[` +
		`{"key":"level","type":"enum","default":"full","options":[{"value":"lite"},{"value":"full"}]}]}`
	base := skillCall(t, `{}`, nil, files).skillKey(context.Background())
	lite := skillCall(t, `{"terse_level":"lite"}`, nil, files).skillKey(context.Background())
	if base == "" || lite == "" || base == lite {
		t.Fatalf("base %q lite %q", base, lite)
	}
}

func sessionCall(t *testing.T, caller identity.Caller, sid string) *call {
	t.Helper()
	h := http.Header{}
	h.Set("X-Session-Id", sid)
	c := skillCall(t, `{}`, h, skillFiles())
	c.in.Caller = caller
	c.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	return c
}

func TestARepeatTurnGetsTheReminder(t *testing.T) {
	caller := identity.Caller{ID: "key-one"}
	first := sessionCall(t, caller, "s1")
	first.applySkills(context.Background())
	first.markSkills(200)
	if !strings.Contains(hubSystem(first), "SIMPLE PROMPT") {
		t.Fatalf("first turn %q", hubSystem(first))
	}
	for turn := range 2 {
		next := sessionCall(t, caller, "s1")
		next.p.d.SkillSessions = first.p.d.SkillSessions
		next.applySkills(context.Background())
		body := hubSystem(next)
		if strings.Contains(body, "SIMPLE PROMPT") {
			t.Fatalf("turn %d carries the full prompt again: %q", turn+2, body)
		}
		if !strings.Contains(body, "Local Skill: Simplifier (already active this conversation") {
			t.Fatalf("turn %d reminder %q", turn+2, body)
		}
	}
}

func TestAnotherCallerKeepsItsOwnSession(t *testing.T) {
	first := sessionCall(t, identity.Caller{ID: "key-one"}, "shared")
	first.applySkills(context.Background())
	first.markSkills(200)
	other := sessionCall(t, identity.Caller{ID: "key-two"}, "shared")
	other.p.d.SkillSessions = first.p.d.SkillSessions
	other.applySkills(context.Background())
	if !strings.Contains(hubSystem(other), "SIMPLE PROMPT") {
		t.Fatalf("another caller inherited the reminder: %q", hubSystem(other))
	}
}

func TestNoSessionHeaderKeepsNoState(t *testing.T) {
	c := skillCall(t, `{}`, nil, skillFiles())
	c.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	c.applySkills(context.Background())
	again := skillCall(t, `{}`, nil, skillFiles())
	again.p.d.SkillSessions = c.p.d.SkillSessions
	again.applySkills(context.Background())
	if !strings.Contains(hubSystem(again), "SIMPLE PROMPT") {
		t.Fatalf("a request without a session id was reminded: %q", hubSystem(again))
	}
}

func TestAFailoverNoticeReachesTheNextTurn(t *testing.T) {
	caller := identity.Caller{ID: "key-one"}
	c := sessionCall(t, caller, "s1")
	key, _, _ := c.sessionInfo()
	c.p.d.SkillSessions.Failover(key)
	next := sessionCall(t, caller, "s1")
	next.p.d.SkillSessions = c.p.d.SkillSessions
	next.applySkills(context.Background())
	if !strings.Contains(hubSystem(next), "Failover Notice") {
		t.Fatalf("notice %q", hubSystem(next))
	}
}

func TestTheSessionKeyRejectsWhatItCannotUse(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", strings.Repeat("s", maxSessionID+1))
	c := skillCall(t, `{}`, h, skillFiles())
	if key, _, _ := c.sessionInfo(); key != "" {
		t.Fatalf("an oversized session id made a key: %q", key)
	}
	h.Set("X-Session-Id", "a\nb")
	c = skillCall(t, `{}`, h, skillFiles())
	if key, _, _ := c.sessionInfo(); key != "" {
		t.Fatalf("a controlled session id made a key: %q", key)
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

func TestReadersReadTheTwoKeysAndTheLegacyKey(t *testing.T) {
	doc, err := document.ParseDoc(`{"terseEnabled":false,"terseRoutingMode":"always","simpleRoutingMode":"sometimes","oldSwitch":true}`)
	if err != nil {
		t.Fatal(err)
	}
	setting, legacy := skill.Readers(doc.GetRaw)
	s := setting("terse")
	if !s.HasEnabled || s.Enabled || !s.HasMode || s.Mode != "always" {
		t.Fatalf("setting %+v", s)
	}
	s = setting("simple")
	if s.HasEnabled || s.HasMode {
		t.Fatalf("a typo was accepted: %+v", s)
	}
	if on, ok := legacy("oldSwitch"); !on || !ok {
		t.Fatalf("legacy %v %v", on, ok)
	}
	if _, ok := legacy("oldSwitchEnabled"); ok {
		t.Fatal("the legacy key had a suffix added")
	}
}

func TestBothBypassHeadersStopEverySkill(t *testing.T) {
	files := map[string]string{
		"always/manifest.json": `{"id":"always","name":"Always","description":"d","hook":"system-prompt","default_enabled":true,"routing_mode":"always"}`,
		"always/prompt.txt":    "ALWAYS PROMPT",
	}
	on := skillCall(t, `{}`, nil, files)
	on.applySkills(context.Background())
	if !strings.Contains(hubSystem(on), "ALWAYS PROMPT") {
		t.Fatalf("the skill was not injected: %q", hubSystem(on))
	}
	h := http.Header{}
	h.Set(skill.BypassLocal, "off")
	h.Set(skill.BypassECC, "off")
	off := skillCall(t, `{}`, h, files)
	off.applySkills(context.Background())
	if got := hubSystem(off); got != "" {
		t.Fatalf("a bypassed request carried %q", got)
	}
}

func TestALegacyEnabledKeyTurnsASkillOnThroughTheRealReader(t *testing.T) {
	files := map[string]string{
		"lint/manifest.json": `{"id":"lint","name":"Lint","description":"d","hook":"system-prompt",` +
			`"legacy_enabled_key":"commitLintEnabled","routing_mode":"always"}`,
		"lint/prompt.txt": "LINT PROMPT",
	}
	on := skillCall(t, `{"commitLintEnabled":true}`, nil, files)
	on.applySkills(context.Background())
	if !strings.Contains(hubSystem(on), "LINT PROMPT") {
		t.Fatal("the legacy key was not read from the settings row")
	}
	own := skillCall(t, `{"commitLintEnabled":true,"lintEnabled":false}`, nil, files)
	own.applySkills(context.Background())
	if strings.Contains(hubSystem(own), "LINT PROMPT") {
		t.Fatal("the legacy key won over the skill's own switch")
	}
	off := skillCall(t, `{}`, nil, files)
	off.applySkills(context.Background())
	if strings.Contains(hubSystem(off), "LINT PROMPT") {
		t.Fatal("a skill with no switch and no default was turned on")
	}
}

// sessionRun asks as one conversation, so the skill session table is touched.
func sessionRun(r *rig, body string) Output {
	r.t.Helper()
	in := Input{
		Path: "/v1/chat/completions", Body: []byte(body), Caller: identity.Caller{ID: "key-1", Name: "ci"}, RequestID: "rid-1",
		Header: http.Header{"X-Session-Id": []string{"s-1"}},
	}
	return r.p.Handle(context.Background(), in, func() (stream.Client, error) { return nil, nil })
}

func TestAFailureThatEndsTheRequestIsNotAFailover(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 400, Body: `{"error":{"message":"bad parameter"}}`})
	r.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	r.conn(nodeID, "k1")
	if out := sessionRun(r, chatBody); out.Status != 400 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if r.p.d.SkillSessions.HadFailover(conversation.SessionKey("key-1", "s-1")) {
		t.Fatal("a request-scoped refusal marked the conversation as failed over")
	}
}

func TestAnAnswerFromASecondAccountIsAFailover(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 429, Body: `{"error":{"message":"rate limit reached"}}`}, tests.Reply{ContentType: "application/json", Body: okJSON})
	r.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	r.conn(nodeID, "k1")
	r.conn(nodeID, "k2")
	if out := sessionRun(r, chatBody); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if !r.p.d.SkillSessions.HadFailover(conversation.SessionKey("key-1", "s-1")) {
		t.Fatal("a conversation that moved to another account was not marked")
	}
}

func TestAComboMovingToItsNextMemberIsNotAFailover(t *testing.T) {
	r := comboRig(t, []string{"wire/short", "forced/fm"}, tests.Reply{Status: 400, Body: `{"error":{"message":"bad parameter"}}`}, codeReply())
	r.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	r.conn("forced", "k1")
	r.conn("wire", "kw")
	if out := sessionRun(r, comboAsk); out.Status != 200 {
		t.Fatalf("%d %s", out.Status, out.Body)
	}
	if r.p.d.SkillSessions.HadFailover(conversation.SessionKey("key-1", "s-1")) {
		t.Fatal("moving to another model of a combo was counted as an account failover")
	}
}

func TestASkillIsRemindedOnlyAfterATurnThatWasAnswered(t *testing.T) {
	r := newRig(t, tests.Reply{Status: 400, Body: `{"error":{"message":"bad parameter"}}`},
		tests.Reply{ContentType: "application/json", Body: okJSON},
		tests.Reply{ContentType: "application/json", Body: okJSON})
	root := t.TempDir()
	for name, body := range map[string]string{
		"simple/manifest.json": `{"id":"simple","name":"Simplifier","description":"smallest change that works",` +
			`"hook":"system-prompt","routable":true,"default_enabled":true,"routing_mode":"smart",` +
			`"triggers":["keep it simple"],"keywords":["refactor","simplify"]}`,
		"simple/prompt.txt": "SIMPLE PROMPT",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.p.d.Skills = skill.NewRegistry(root, nil)
	r.p.d.Settings = settingsRow{raw: `{}`}
	r.p.d.SkillSessions = conversation.NewSkillSessions(nil)
	r.conn(nodeID, "k1")
	ask := chatJSON("m", "please refactor this, keep it simple")
	for _, want := range []struct {
		status int
		full   bool
	}{{400, true}, {200, true}, {200, false}} {
		out := sessionRun(r, ask)
		if out.Status != want.status {
			t.Fatalf("%d %s", out.Status, out.Body)
		}
		calls := r.up.Calls()
		body := string(calls[len(calls)-1].Body)
		if got := strings.Contains(body, "SIMPLE PROMPT"); got != want.full {
			t.Fatalf("turn answered %d carries the full prompt %v, want %v: %s", want.status, got, want.full, body)
		}
	}
}

func TestTheCacheKeyFollowsWhatTheRequestWouldCarry(t *testing.T) {
	ctx := context.Background()
	row := `{"terseEnabled":true,"simpleEnabled":true}`
	base := skillCall(t, row, nil, skillFiles()).skillKey(ctx)

	localOff := skillCall(t, row, bypassHeader(skill.BypassLocal), skillFiles()).skillKey(ctx)
	if localOff == base {
		t.Error("a request that switched the local router off shares the key of one that kept it on")
	}

	// A conversation that already carried a smart skill is reminded of it, and
	// one that failed over carries the notice: neither request is the first one.
	seen := sessionCall(t, identity.Caller{ID: "key-one"}, "s1")
	seen.p.d.Settings = settingsRow{raw: row}
	fresh := seen.skillKey(ctx)
	seen2 := sessionCall(t, identity.Caller{ID: "key-one"}, "s1")
	seen2.p.d.Settings = settingsRow{raw: row}
	seen2.p.d.SkillSessions.Mark(conversation.SessionKey("key-one", "s1"), []string{"local-simple"})
	if reminded := seen2.skillKey(ctx); reminded == fresh {
		t.Error("a conversation that already carried a skill shares the key of a first turn")
	}
	seen3 := sessionCall(t, identity.Caller{ID: "key-one"}, "s1")
	seen3.p.d.Settings = settingsRow{raw: row}
	seen3.p.d.SkillSessions.Failover(conversation.SessionKey("key-one", "s1"))
	if failed := seen3.skillKey(ctx); failed == fresh {
		t.Error("a conversation that failed over shares the key of one that did not")
	}
}
