package controllers

import (
	"bufio"
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/app/access/auth"
	"rllmx/app/gateway/skill"
	apphttp "rllmx/app/http"
	"rllmx/app/platform/logbook"
	"rllmx/app/store/repository"
)

func (m *mgmtRig) settingsRoutes(c *SettingsController) {
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/settings", c.Show)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodPatch, "/api/settings", c.Update)
	m.guard.Handle(m.router, apphttp.ClassPublic, nethttp.MethodPost, "/api/locale", c.Locale)
}

func (m *mgmtRig) logsRoutes(c *LogsController) {
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/translator/console-logs", c.List)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodDelete, "/api/translator/console-logs", c.Clear)
	m.guard.Handle(m.router, apphttp.ClassAlwaysProtected, nethttp.MethodGet, "/api/translator/console-logs/stream", c.Stream)
}

// seededSettings stores a password, a sealed secret and a key this build does
// not know, so the view can be checked against all three.
func seededSettings(t *testing.T, m *mgmtRig) {
	t.Helper()
	ctx := context.Background()
	if err := m.st.Store.Settings().SetPasswordHash(ctx, "stored-hash"); err != nil {
		t.Fatal(err)
	}
	if err := m.st.Store.Settings().Patch(ctx, map[string]string{
		"oidcClientSecret": `"s3cret-value"`,
		"unlistedSetting":  `"keep-out"`,
		"requireLogin":     "true",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTheSettingsViewCarriesOnlyTheAllowList(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})

	code, out, raw := m.call(nethttp.MethodGet, "/api/settings", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, secret := range []string{"s3cret-value", "stored-hash", "unlistedSetting", "keep-out", `"password":`} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the view carries %q: %s", secret, raw)
		}
	}
	if out["requireLogin"] != true || out["hasPassword"] != true || out["authMode"] != "password" {
		t.Fatalf("%v", out)
	}
	if _, ok := out["comboStrategies"]; !ok {
		t.Fatalf("a listed setting is missing: %v", out)
	}
}

// The three skill groups are kill switches: on by default, written and read
// back like any other listed setting.
func TestTheSkillGroupSwitchesRoundTrip(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	code, out, raw := m.call(nethttp.MethodGet, "/api/settings", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for _, key := range []string{"builtinSkillsEnabled", "customSkillsEnabled", "eccSkillsEnabled"} {
		if out[key] != true {
			t.Errorf("%s starts %v", key, out[key])
		}
	}
	body := `{"builtinSkillsEnabled":false,"customSkillsEnabled":false,"eccSkillsEnabled":false}`
	if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	_, out, _ = m.call(nethttp.MethodGet, "/api/settings", "", true)
	for _, key := range []string{"builtinSkillsEnabled", "customSkillsEnabled", "eccSkillsEnabled"} {
		if out[key] != false {
			t.Errorf("%s did not persist: %v", key, out[key])
		}
	}
}

func TestASettingsWriteRefusesWhatItCannotValidateAndWritesNothing(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	for name, body := range map[string]string{
		"unknown key":    `{"whatever":true}`,
		"null value":     `{"requireLogin":null}`,
		"null text":      `{"oidcClientId":null}`,
		"wrong type":     `{"requireLogin":"yes"}`,
		"unknown mode":   `{"authMode":"none"}`,
		"bad strategy":   `{"comboStrategy":"random"}`,
		"bad cache":      `{"responseCache":{"enabled":true,"ttlSeconds":0}}`,
		"unknown cache":  `{"responseCache":{"enabled":true,"extra":1}}`,
		"bad combo knob": `{"comboStrategies":{"fast":{"fallbackStrategy":"random"}}}`,
		"plain http sso": `{"oidcIssuerUrl":"http://idp.example"}`,
		// Discovery refuses these, so storing one would hide the password form
		// for a login that can never finish.
		"issuer path":     `{"oidcIssuerUrl":"https://idp.example.com/tenant/v2"}`,
		"issuer query":    `{"oidcIssuerUrl":"https://idp.example.com?tenant=1"}`,
		"issuer userinfo": `{"oidcIssuerUrl":"https://u:p@idp.example.com"}`,
		"issuer scheme":   `{"oidcIssuerUrl":"ftp://idp.example.com"}`,
		"bad headroom":    `{"headroomUrl":"ftp://sidecar:8787"}`,
		"headroom user":   `{"headroomUrl":"http://u:p@sidecar:8787"}`,
		"headroom wait":   `{"headroomTimeoutMs":5}`,
		"bad caveman":     `{"cavemanLevel":"extreme"}`,
		"bad mode":        `{"ponytailRoutingMode":"sometimes"}`,
		"long prompt":     `{"customPrompt":"` + strings.Repeat("a", 4001) + `"}`,
		"password":        `{"newPassword":"a-good-long-password"}`,
		"not an object":   `[]`,
		"empty":           `{}`,
	} {
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := d.GetString("authMode"); v == "none" {
		t.Fatal("a refused value was stored")
	}
}

// The CLI and discovery canonicalize the issuer; the API must store the same
// string, and accept the loopback http address the design allows.
func TestTheIssuerIsStoredAsDiscoveryWillReadIt(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	for in, want := range map[string]string{
		"https://IDP.Example.com":  "https://idp.example.com",
		"https://idp.example.com/": "https://idp.example.com",
		"http://localhost:8080":    "http://localhost:8080",
		"":                         "",
	} {
		body, _ := json.Marshal(map[string]string{"oidcIssuerUrl": in})
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", string(body), true); code != 200 {
			t.Fatalf("%q: %d %s", in, code, raw)
		}
		d, err := m.st.Store.Settings().Effective(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := d.GetString("oidcIssuerUrl"); got != want {
			t.Errorf("%q stored as %q, want %q", in, got, want)
		}
	}
}

func TestProviderStrategiesTakeTheSelectorShape(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})

	for name, body := range map[string]string{
		"unknown strategy":  `{"providerStrategies":{"openai":{"fallbackStrategy":"random"}}}`,
		"sticky zero":       `{"providerStrategies":{"openai":{"stickyRoundRobinLimit":0}}}`,
		"sticky too large":  `{"providerStrategies":{"openai":{"stickyRoundRobinLimit":101}}}`,
		"sticky as text":    `{"providerStrategies":{"openai":{"stickyRoundRobinLimit":"3"}}}`,
		"sticky a fraction": `{"providerStrategies":{"openai":{"stickyRoundRobinLimit":3.5}}}`,
		"strategy as bool":  `{"providerStrategies":{"openai":{"fallbackStrategy":true}}}`,
		"bare number":       `{"providerStrategies":{"openai":3}}`,
		"bare array":        `{"providerStrategies":{"openai":[]}}`,
		"bad provider name": `{"providerStrategies":{"open/ai":{"fallbackStrategy":"fill-first"}}}`,
		// Combo words the selector does not act on: accepting them stores a
		// strategy that behaves as fill-first.
		"combo word in object": `{"providerStrategies":{"openai":{"fallbackStrategy":"smart"}}}`,
		"combo word bare":      `{"providerStrategies":{"openai":"fallback"}}`,
	} {
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}

	code, _, raw := m.call(nethttp.MethodPatch, "/api/settings",
		`{"providerStrategies":{"openai":{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":4,"proxyPoolId":"p1"},"gh":"fill-first"}}`, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := d.GetRaw("providerStrategies")
	if !ok {
		t.Fatal("providerStrategies was not stored")
	}
	checks := map[string]string{
		"openai.fallbackStrategy": "round-robin",
		"openai.proxyPoolId":      "p1",
		"gh":                      "fill-first",
	}
	for path, want := range checks {
		if got := gjson.Get(raw, path).String(); got != want {
			t.Errorf("%s = %q, want %q (%s)", path, got, want, raw)
		}
	}
	if n := gjson.Get(raw, "openai.stickyRoundRobinLimit").Int(); n != 4 {
		t.Errorf("sticky limit = %d (%s)", n, raw)
	}
}

func TestASettingsWriteStoresAValidChangeAndAppliesItAtOnce(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	authStore := auth.NewStore(func() *repository.Store { return m.st.Store })
	m.st.Store.OnSettingsChange(authStore.Changed)
	if !authStore.Get(context.Background()).RequireLogin {
		t.Fatal("the guard did not start strict")
	}
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})

	code, out, raw := m.call(nethttp.MethodPatch, "/api/settings",
		`{"requireLogin":false,"responseCache":{"enabled":true,"ttlSeconds":60,"maxEntryKb":64},`+
			`"comboStrategies":{"fast":{"fallbackStrategy":"round-robin","fusionTuning":{"minPanel":3}}}}`, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if out["requireLogin"] != false {
		t.Fatalf("%v", out)
	}
	if authStore.Get(context.Background()).RequireLogin {
		t.Fatal("the guard kept its cached settings until the TTL")
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetRaw("responseCache"); !strings.Contains(got, `"ttlSeconds":60`) {
		t.Fatalf("cache switch %s", got)
	}
}

func TestASettingsWriteStoresTheSaverKeysAndAnswersThem(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	code, out, raw := m.call(nethttp.MethodPatch, "/api/settings",
		`{"rtkEnabled":false,"headroomEnabled":true,"headroomUrl":"http://localhost:8787",`+
			`"headroomCompressUserMessages":true,"headroomTimeoutMs":1500,`+
			`"cavemanEnabled":true,"cavemanLevel":"wenyan-lite","cavemanRoutingMode":"smart",`+
			`"ponytailEnabled":true,"ponytailLevel":"ultra","ponytailRoutingMode":"always",`+
			`"customPromptEnabled":true,"customPrompt":"Answer in one paragraph."}`, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if out["rtkEnabled"] != false || out["cavemanLevel"] != "wenyan-lite" || out["headroomTimeoutMs"] != float64(1500) {
		t.Fatalf("%v", out)
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetString("ponytailRoutingMode"); got != "always" {
		t.Fatalf("ponytailRoutingMode %q", got)
	}
}

// terseRegistry is a registry holding one custom skill, "terse".
func terseRegistry(t *testing.T) *skill.Registry {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"terse/manifest.json": `{"id":"terse","name":"Terse","description":"short","hook":"system-prompt",` +
			`"legacy_enabled_key":"terseLegacyOn"}`,
		"terse/prompt.txt": "Answer briefly.",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return skill.NewRegistry(dir, nil)
}

func TestASettingsWriteAcceptsAPerSkillSwitch(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth, Skills: terseRegistry(t)})
	code, _, raw := m.call(nethttp.MethodPatch, "/api/settings",
		`{"terseEnabled":true,"terseLegacyOn":false,"terseRoutingMode":"always","skillSources":["https://github.com/acme/skills"],`+
			`"ecc_auto_skill_routerEnabled":true,"ecc_auto_skill_routerConfidence":0.5,"ecc_auto_skill_routerMaxSkills":2}`, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetString("terseRoutingMode"); got != "always" {
		t.Fatalf("terseRoutingMode %q", got)
	}
	if got, _ := d.GetRaw("skillSources"); !strings.Contains(got, "github.com/acme/skills") {
		t.Fatalf("skillSources %s", got)
	}
	if got, _ := d.GetRaw("ecc_auto_skill_routerConfidence"); got != "0.5" {
		t.Fatalf("confidence %s", got)
	}
}

func TestASettingsWriteAcceptsASkillConfigField(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	dir := t.TempDir()
	for name, body := range map[string]string{
		"terse/manifest.json": `{"id":"terse","name":"Terse","description":"short","hook":"system-prompt",` +
			`"config_schema":[{"key":"level","type":"enum","default":"full","options":[{"value":"lite"},{"value":"full"}]}]}`,
		"terse/prompt.txt": "{level} answers",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg := skill.NewRegistry(dir, nil)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth, Skills: reg})
	if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", `{"terse_level":"lite"}`, true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetString("terse_level"); got != "lite" {
		t.Fatalf("terse_level %q", got)
	}
	for name, body := range map[string]string{
		"not an option": `{"terse_level":"ultra"}`,
		"wrong type":    `{"terse_level":3}`,
		"unknown field": `{"terse_missing":"x"}`,
	} {
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

func TestASettingsWriteRefusesABadSkillKey(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth, Skills: terseRegistry(t)})
	for name, body := range map[string]string{
		"bad mode":       `{"terseRoutingMode":"sometimes"}`,
		"plain http":     `{"skillSources":["http://github.com/acme/skills"]}`,
		"user info":      `{"skillSources":["https://u:p@github.com/acme/skills"]}`,
		"confidence":     `{"ecc_auto_skill_routerConfidence":1.5}`,
		"max skills":     `{"ecc_auto_skill_routerMaxSkills":9}`,
		"parent path":    `{"../terseEnabled":true}`,
		"upper-case id":  `{"TerseEnabled":true}`,
		"not a skill":    `{"tunnelEnabled":true}`,
		"legacy not on":  `{"terseLegacyOn":"yes"}`,
		"foreign legacy": `{"otherLegacyOn":true}`,
		"not installed":  `{"ghostRoutingMode":"smart"}`,
	} {
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

func TestTheAutoPlanSettingsAreValidatedAndRead(t *testing.T) {
	m := newMgmtRig(t)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})

	code, out, raw := m.call(nethttp.MethodGet, "/api/settings", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	for key, want := range map[string]any{
		"autoPlanEnabled":             false,
		"autoPlanMode":                "auto",
		"autoPlanComboId":             "",
		"autoCodeComboId":             "",
		"autoPlanComplexityThreshold": float64(6),
		"autoPlanSmartClassify":       false,
		"autoPlanMaxTokens":           float64(800),
		"autoPlanTimeoutMs":           float64(15000),
		"autoPlanShowInResponse":      false,
	} {
		if out[key] != want {
			t.Errorf("%s = %v, want %v", key, out[key], want)
		}
	}

	body := `{"autoPlanEnabled":true,"autoPlanMode":"manual","autoPlanComboId":"planner",` +
		`"autoCodeComboId":"coder","autoPlanComplexityThreshold":8,"autoPlanSmartClassify":true,` +
		`"autoPlanMaxTokens":1200,"autoPlanTimeoutMs":20000,"autoPlanShowInResponse":true}`
	code, out, raw = m.call(nethttp.MethodPatch, "/api/settings", body, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	if out["autoPlanMode"] != "manual" || out["autoPlanComboId"] != "planner" || out["autoPlanMaxTokens"] != float64(1200) {
		t.Fatalf("%v", out)
	}

	for name, body := range map[string]string{
		"bad mode":       `{"autoPlanMode":"sometimes"}`,
		"low threshold":  `{"autoPlanComplexityThreshold":0}`,
		"high threshold": `{"autoPlanComplexityThreshold":11}`,
		"few tokens":     `{"autoPlanMaxTokens":199}`,
		"many tokens":    `{"autoPlanMaxTokens":4001}`,
		"short timeout":  `{"autoPlanTimeoutMs":999}`,
		"long combo id":  `{"autoPlanComboId":"` + strings.Repeat("a", 129) + `"}`,
		"control char":   `{"autoCodeComboId":"a\nb"}`,
	} {
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

func TestABlankSSOSecretKeepsTheStoredOne(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", `{"oidcClientSecret":""}`, true); code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	d, err := m.st.Store.Settings().Effective(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := d.GetString("oidcClientSecret"); got != "s3cret-value" {
		t.Fatalf("the secret became %q", got)
	}
}

func TestTheSettingsRoutesNeedASessionEvenWhenLoginIsOptional(t *testing.T) {
	m := newMgmtRig(t)
	if err := m.st.Store.Settings().Patch(context.Background(), map[string]string{"requireLogin": "false"}); err != nil {
		t.Fatal(err)
	}
	m.auth.Store.Changed()
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	if code, _, _ := m.call(nethttp.MethodGet, "/api/settings", "", false); code != nethttp.StatusUnauthorized {
		t.Fatalf("%d without a session", code)
	}
}

func TestLocaleSetsACookieAndRefusesAnUnknownLanguage(t *testing.T) {
	m := newMgmtRig(t)
	m.settingsRoutes(&SettingsController{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(nethttp.MethodPost, "/api/locale", strings.NewReader(`{"locale":"id"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr, req.Host = localPeer, localHost
	m.route.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Set-Cookie"), "locale=id") {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Set-Cookie"))
	}
	if code, _, _ := m.call(nethttp.MethodPost, "/api/locale", `{"locale":"fr"}`, false); code != 400 {
		t.Fatalf("%d for an unknown locale", code)
	}
}

func TestTheConsoleLogListsClearsAndStreams(t *testing.T) {
	m := newMgmtRig(t)
	b := logbook.New(8)
	b.Add("info", time.Now(), "first line")
	lc := &LogsController{Book: b, every: 10 * time.Millisecond, beat: 40 * time.Millisecond}
	m.logsRoutes(lc)

	code, out, raw := m.call(nethttp.MethodGet, "/api/translator/console-logs", "", true)
	if code != 200 || out["success"] != true {
		t.Fatalf("%d %s", code, raw)
	}
	lines, _ := out["logs"].([]any)
	if len(lines) != 1 || !strings.Contains(raw, "first line") {
		t.Fatalf("%s", raw)
	}

	srv := httptest.NewServer(m.route)
	defer srv.Close()
	req, err := nethttp.NewRequest(nethttp.MethodGet, srv.URL+"/api/translator/console-logs/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: m.cookie})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	br := bufio.NewReader(resp.Body)
	first, err := br.ReadString('\n')
	if err != nil || !strings.Contains(first, `"type":"init"`) || !strings.Contains(first, "first line") {
		t.Fatalf("first frame %q (%v)", first, err)
	}
	b.Add("warning", time.Now(), "second line")
	seen := false
	for i := 0; i < 40 && !seen; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended early: %v", err)
		}
		seen = strings.Contains(line, `"type":"lines"`) && strings.Contains(line, "second line")
	}
	if !seen {
		t.Fatal("the new line never arrived")
	}
	b.Clear()
	cleared := false
	for i := 0; i < 40 && !cleared; i++ {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended early: %v", err)
		}
		cleared = strings.Contains(line, `"type":"clear"`)
	}
	if !cleared {
		t.Fatal("no clear frame arrived")
	}

	if code, _, _ := m.call(nethttp.MethodDelete, "/api/translator/console-logs", "", true); code != 200 {
		t.Fatalf("%d for a clear", code)
	}
	if code, _, _ := m.call(nethttp.MethodGet, "/api/translator/console-logs", "", true); code != 200 {
		t.Fatalf("%d after a clear", code)
	}
}

func TestTheConsoleLogNeedsASession(t *testing.T) {
	m := newMgmtRig(t)
	m.logsRoutes(&LogsController{Book: logbook.New(8)})
	if code, _, _ := m.call(nethttp.MethodGet, "/api/translator/console-logs", "", false); code != nethttp.StatusUnauthorized {
		t.Fatalf("%d without a session", code)
	}
}

func TestAnAutoPingSettingKeepsTheReferenceShape(t *testing.T) {
	m := newMgmtRig(t)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	body := `{"claudeAutoPing":{"connections":{"c1":true},"schedules":{"c1":{"enabled":true,"anchor":"09:30","windowHours":5,"slotsPerDay":3,"timezone":"Asia/Jakarta"}},"note":"kept"}}`
	code, out, raw := m.call(nethttp.MethodPatch, "/api/settings", body, true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	stored := out["claudeAutoPing"].(map[string]any)
	if stored["note"] != "kept" {
		t.Fatalf("an unknown key was dropped: %v", stored)
	}
	if stored["connections"].(map[string]any)["c1"] != true {
		t.Fatalf("the opt-in was lost: %v", stored)
	}
	if out["codexAutoPing"] != false {
		t.Fatalf("an unset switch reads %v", out["codexAutoPing"])
	}

	code, out, raw = m.call(nethttp.MethodPatch, "/api/settings", `{"claudeAutoPing":true}`, true)
	if code != 200 || out["claudeAutoPing"] != true {
		t.Fatalf("a switch %d %s", code, raw)
	}

	for _, bad := range []string{
		`{"codexAutoPing":"yes"}`,
		`{"codexAutoPing":{"connections":{"c1":"on"}}}`,
		`{"codexAutoPing":{"schedules":{"c1":{"anchor":"25:00"}}}}`,
		`{"codexAutoPing":{"schedules":{"c1":{"windowHours":0.1}}}}`,
		`{"codexAutoPing":{"schedules":{"c1":{"windowHours":5,"slotsPerDay":9}}}}`,
	} {
		code, _, raw = m.call(nethttp.MethodPatch, "/api/settings", bad, true)
		if code != 400 {
			t.Fatalf("%s was stored: %d %s", bad, code, raw)
		}
	}
}

// The account selector reads fallbackStrategy, so the settings write has to
// accept the same two names it acts on and refuse the combo words.
func TestASettingsWriteStoresTheAccountStrategy(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	for _, value := range []string{"round-robin", "fill-first"} {
		code, out, raw := m.call(nethttp.MethodPatch, "/api/settings", `{"fallbackStrategy":"`+value+`"}`, true)
		if code != 200 {
			t.Fatalf("%s: %d %s", value, code, raw)
		}
		if out["fallbackStrategy"] != value {
			t.Fatalf("%s: the view answered %v", value, out["fallbackStrategy"])
		}
	}
	code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", `{"fallbackStrategy":"smart"}`, true)
	if code != 400 {
		t.Fatalf("a combo word was stored: %d %s", code, raw)
	}
}

func TestACustomPromptKeepsItsLineBreaksButNotOtherControls(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	code, out, raw := m.call(nethttp.MethodPatch, "/api/settings", `{"customPrompt":"Line one.\r\n\tLine two."}`, true)
	if code != 200 || out["customPrompt"] != "Line one.\r\n\tLine two." {
		t.Fatalf("a multi-line prompt: %d %s", code, raw)
	}
	for _, bad := range []string{`"a\u0000b"`, `"a\u001bb"`, `"a\u0085b"`} {
		if code, _, raw := m.call(nethttp.MethodPatch, "/api/settings", `{"customPrompt":`+bad+`}`, true); code != 400 {
			t.Fatalf("%s was stored: %d %s", bad, code, raw)
		}
	}
	long := strings.Repeat("x", 4001)
	if code, _, _ := m.call(nethttp.MethodPatch, "/api/settings", `{"customPrompt":"`+long+`"}`, true); code != 400 {
		t.Fatalf("an oversized prompt was stored: %d", code)
	}
}

// An imported database can hold a strategy word this program does not act on.
// The page sends the value it was given back, so the view must show the one the
// router uses, or the routing card could never be saved.
func TestTheViewShowsTheStrategyTheRouterUses(t *testing.T) {
	m := newMgmtRig(t)
	seededSettings(t, m)
	if err := m.st.Store.Settings().Patch(context.Background(), map[string]string{"fallbackStrategy": `"smart"`}); err != nil {
		t.Fatal(err)
	}
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	code, out, raw := m.call(nethttp.MethodGet, "/api/settings", "", true)
	if code != 200 || out["fallbackStrategy"] != "fill-first" {
		t.Fatalf("%d %s", code, raw)
	}
}

// A log line the logger writes must reach the page while the stream is open,
// through the compression wrapper the server puts around every handler: held in
// its buffer, the page would show nothing until a whole kilobyte had built up.
func TestAConsoleLineReachesTheBrowserThroughTheCompressionWrapper(t *testing.T) {
	m := newMgmtRig(t)
	b := logbook.New(8)
	m.logsRoutes(&LogsController{Book: b, every: 10 * time.Millisecond, beat: time.Minute})

	srv := httptest.NewServer(apphttp.Compress(true, m.route))
	defer srv.Close()
	req, err := nethttp.NewRequest(nethttp.MethodGet, srv.URL+"/api/translator/console-logs/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&nethttp.Cookie{Name: auth.CookieName, Value: m.cookie})
	req.Header.Set("Accept-Encoding", "gzip, br")
	resp, err := srv.Client().Transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("a stream was encoded: %q", resp.Header.Get("Content-Encoding"))
	}

	got := make(chan string, 1)
	go func() {
		br := bufio.NewReader(resp.Body)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			if strings.Contains(line, "live line") {
				got <- line
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	b.Add("info", time.Now(), "live line")
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("a small frame was held back instead of reaching the browser")
	}
}

// The page says "using the default password" when anyone could sign in with a
// public one: no hash stored, or a hash of a default. A password someone chose
// is not that, even though a hash is stored.
func TestTheSettingsViewSaysWhetherThePasswordIsADefault(t *testing.T) {
	m := newMgmtRig(t)
	m.settingsRoutes(&SettingsController{DB: func() *repository.Store { return m.st.Store }, Auth: m.auth})
	ctx := context.Background()
	read := func() map[string]any {
		_, out, raw := m.call(nethttp.MethodGet, "/api/settings", "", true)
		if _, ok := out["defaultPassword"]; !ok {
			t.Fatalf("the view has no defaultPassword: %s", raw)
		}
		return out
	}
	if out := read(); out["defaultPassword"] != true || out["hasPassword"] != false {
		t.Fatalf("no password stored: %v", out)
	}
	if err := m.st.Store.Settings().SetPasswordHash(ctx, "hashed:"+auth.DefaultPassword); err != nil {
		t.Fatal(err)
	}
	if out := read(); out["defaultPassword"] != true || out["hasPassword"] != true {
		t.Fatalf("a hash of the default: %v", out)
	}
	if err := m.st.Store.Settings().SetPasswordHash(ctx, "hashed:a-password-someone-chose"); err != nil {
		t.Fatal(err)
	}
	if out := read(); out["defaultPassword"] != false || out["hasPassword"] != true {
		t.Fatalf("a chosen password: %v", out)
	}
}
