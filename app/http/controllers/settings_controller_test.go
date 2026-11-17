package controllers

import (
	"bufio"
	"context"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rllmx/app/access/auth"
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
		"password":       `{"newPassword":"a-good-long-password"}`,
		"not an object":  `[]`,
		"empty":          `{}`,
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
	b.Add("warn", time.Now(), "second line")
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
