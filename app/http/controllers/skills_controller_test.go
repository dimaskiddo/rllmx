package controllers

import (
	"context"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rllmx/app/gateway/skill"
	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

func skillRegistry(t *testing.T) *skill.Registry {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"terse/manifest.json": `{"id":"terse","name":"Terse","description":"short answers",` +
			`"hook":"system-prompt","default_enabled":true,"routing_mode":"always"}`,
		"terse/prompt.txt": "PROMPT TEXT THAT MUST NOT LEAVE",
		"simple/manifest.json": `{"id":"simple","name":"Simplifier","description":"smallest change",` +
			`"hook":"system-prompt","routable":true,"default_enabled":true,"routing_mode":"smart"}`,
		"simple/prompt.txt":    "SIMPLE PROMPT",
		"broken/manifest.json": `{"id":"broken","name":"","description":"","hook":"nope"}`,
		"broken/prompt.txt":    "text",
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

func skillsRoutes(m *mgmtRig, c *SkillsController) {
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/skills", c.List)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/skills/sync", c.Sync)
}

func studioRoutes(m *mgmtRig, c *SkillsController) {
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/skills", c.List)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodPost, "/api/skills", c.Create)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/skills/{id}", c.Get)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodPut, "/api/skills/{id}", c.Update)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/skills/{id}", c.Delete)
}

func TestTheStudioCreatesReadsUpdatesAndDeletes(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	dir := t.TempDir()
	// The registry reads DATA_DIR/skills and the controller is given DATA_DIR, as
	// the server wires them.
	reg := skill.NewRegistry(filepath.Join(dir, "skills"), skill.Builtin())
	c := &SkillsController{DB: func() *repository.Store { return m.st.Store }, Reg: reg, Dir: dir}
	studioRoutes(m, c)

	body := `{"id":"mine","name":"Mine","description":"a custom skill","hook":"system-prompt",` +
		`"routing_mode":"always","prompt_template":"MY PROMPT"}`
	if code, _, raw := m.call(nethttp.MethodPost, "/api/skills", body, true); code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	code, out, raw := m.call(nethttp.MethodGet, "/api/skills/mine", "", true)
	if code != 200 || out["promptTemplate"] != "MY PROMPT" || out["editable"] != true {
		t.Fatalf("get: %d %s", code, raw)
	}
	if code, _, raw := m.call(nethttp.MethodPost, "/api/skills", body, true); code != 409 {
		t.Fatalf("duplicate create: %d %s", code, raw)
	}
	update := `{"id":"mine","name":"Mine Again","description":"a custom skill","hook":"system-prompt",` +
		`"prompt_template":"SECOND PROMPT"}`
	if code, _, raw := m.call(nethttp.MethodPut, "/api/skills/mine", update, true); code != 200 {
		t.Fatalf("update: %d %s", code, raw)
	}
	if code, out, raw := m.call(nethttp.MethodGet, "/api/skills/mine", "", true); out["promptTemplate"] != "SECOND PROMPT" {
		t.Fatalf("reads back %d %s", code, raw)
	}
	if code, _, raw := m.call(nethttp.MethodDelete, "/api/skills/mine", "", true); code != 200 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	if code, _, raw := m.call(nethttp.MethodGet, "/api/skills/mine", "", true); code != 404 {
		t.Fatalf("after delete: %d %s", code, raw)
	}
}

func TestTheStudioRefusesWhatItMayNotWrite(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	dir := t.TempDir()
	writeFiles := func(name, body string) {
		p := filepath.Join(dir, "skills", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles("hand/manifest.json", `{"id":"hand","name":"Hand","description":"placed by hand","hook":"system-prompt"}`)
	writeFiles("hand/prompt.txt", "text")
	reg := skill.NewRegistry(filepath.Join(dir, "skills"), skill.Builtin())
	c := &SkillsController{DB: func() *repository.Store { return m.st.Store }, Reg: reg, Dir: dir}
	studioRoutes(m, c)

	base := `"name":"X","description":"d","hook":"system-prompt","prompt_template":"p"`
	for name, c := range map[string]struct {
		method, path, body string
		code               int
	}{
		"traversal id":   {nethttp.MethodPost, "/api/skills", `{"id":"../escape",` + base + `}`, 400},
		"built-in id":    {nethttp.MethodPut, "/api/skills/caveman-style", `{"id":"caveman-style",` + base + `}`, 400},
		"hook script":    {nethttp.MethodPost, "/api/skills", `{"id":"x",` + base + `,"hook_script":"process.exit(1)"}`, 400},
		"install":        {nethttp.MethodPost, "/api/skills", `{"id":"x",` + base + `,"install_command":"npm i"}`, 400},
		"source":         {nethttp.MethodPost, "/api/skills", `{"id":"x",` + base + `,"source":"ecc"}`, 400},
		"hand-written":   {nethttp.MethodPut, "/api/skills/hand", `{"id":"hand",` + base + `}`, 403},
		"catalog folder": {nethttp.MethodDelete, "/api/skills/ecc-imported", "", 400},
		"missing":        {nethttp.MethodGet, "/api/skills/nope", "", 404},
	} {
		if got, _, raw := m.call(c.method, c.path, c.body, true); got != c.code {
			t.Errorf("%s: %d %s", name, got, raw)
		}
	}
}

func TestTheSkillsListCarriesTheEffectiveSwitch(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	if err := m.st.Store.Settings().Patch(context.Background(), map[string]string{
		"terseEnabled": `false`, "simpleRoutingMode": `"always"`,
	}); err != nil {
		t.Fatal(err)
	}
	c := &SkillsController{DB: func() *repository.Store { return m.st.Store }, Reg: skillRegistry(t), Dir: t.TempDir()}
	skillsRoutes(m, c)
	code, out, raw := m.call(nethttp.MethodGet, "/api/skills", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if strings.Contains(raw, "PROMPT TEXT THAT MUST NOT LEAVE") {
		t.Fatalf("prompt text left the route: %s", raw)
	}
	list, _ := out["skills"].([]any)
	if len(list) != 2 {
		t.Fatalf("skills %v", out["skills"])
	}
	seen := map[string]map[string]any{}
	for _, item := range list {
		row, _ := item.(map[string]any)
		seen[row["id"].(string)] = row
	}
	if seen["terse"]["enabled"] != false || seen["terse"]["mode"] != "always" {
		t.Fatalf("terse %v", seen["terse"])
	}
	if seen["simple"]["enabled"] != true || seen["simple"]["mode"] != "always" {
		t.Fatalf("simple %v", seen["simple"])
	}
	if problems, _ := out["problems"].([]any); len(problems) != 1 {
		t.Fatalf("problems %v", out["problems"])
	}
}

func TestTheSkillSyncRefusesWhatTheRouteMayNotDo(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	c := &SkillsController{DB: func() *repository.Store { return m.st.Store }, Reg: skillRegistry(t), Dir: t.TempDir()}
	skillsRoutes(m, c)
	for name, body := range map[string]string{
		"not json":   `nope`,
		"local path": `{"source":"/etc/skills"}`,
		"plain http": `{"source":"http://example.com/x"}`,
		"not listed": `{"source":"https://github.com/acme/skills"}`,
	} {
		code, _, raw := m.call(nethttp.MethodPost, "/api/skills/sync", body, true)
		if code != 400 && code != 502 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

func TestTheSkillsRoutesNeedASession(t *testing.T) {
	m := newMgmtRig(t)
	c := &SkillsController{DB: func() *repository.Store { return m.st.Store }, Reg: skillRegistry(t), Dir: t.TempDir()}
	skillsRoutes(m, c)
	for _, call := range []struct{ method, path string }{
		{nethttp.MethodGet, "/api/skills"},
		{nethttp.MethodPost, "/api/skills/sync"},
	} {
		if code, _, _ := m.call(call.method, call.path, "", false); code != 401 {
			t.Errorf("%s %s: %d", call.method, call.path, code)
		}
	}
}

func TestTheSkillsListCarriesTheStoredConfigValues(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	root := t.TempDir()
	for name, body := range map[string]string{
		"tuned/manifest.json": `{"id":"tuned","name":"Tuned","description":"d","hook":"system-prompt","config_schema":[` +
			`{"key":"level","type":"enum","default":"full","options":[{"value":"lite"},{"value":"full"}]},` +
			`{"key":"scale","type":"slider","default":3,"min":1,"max":10,"legacy_key":"tunedScaleLegacy"},` +
			`{"key":"loud","type":"toggle","default":false},` +
			`{"key":"untouched","type":"text","default":"x"}]}`,
		"tuned/prompt.txt": "{level}",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.st.Store.Settings().Patch(context.Background(), map[string]string{
		"tuned_level": `"lite"`, "tunedScaleLegacy": `7`, "tuned_loud": `true`,
	}); err != nil {
		t.Fatal(err)
	}
	c := &SkillsController{DB: func() *repository.Store { return m.st.Store }, Reg: skill.NewRegistry(root, nil), Dir: t.TempDir()}
	skillsRoutes(m, c)
	code, out, raw := m.call(nethttp.MethodGet, "/api/skills", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	list, _ := out["skills"].([]any)
	row, _ := list[0].(map[string]any)
	values, _ := row["values"].(map[string]any)
	if values["level"] != "lite" || values["scale"] != float64(7) || values["loud"] != true {
		t.Fatalf("values %v", row["values"])
	}
	if _, set := values["untouched"]; set {
		t.Fatalf("a field nobody set carries a value: %v", values)
	}
}
