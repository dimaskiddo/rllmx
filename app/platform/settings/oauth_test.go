package settings

import (
	"maps"
	"strings"
	"testing"
	"time"

	"rllmx/tests"
)

func oauthConfig(over map[string]any) tests.Config {
	v := map[string]any{"oauth.callback_host": "127.0.0.1", "oauth.background_refresh": true, "oauth.refresh_delay": "1500ms"}
	maps.Copy(v, over)
	return tests.Config{Values: v}
}

func TestOAuthSettings(t *testing.T) {
	o, err := LoadOAuth(oauthConfig(map[string]any{"oauth.claude_client_id": " abc "}))
	if err != nil {
		t.Fatal(err)
	}
	if o.ClientID("claude") != "abc" || o.ClientID("codex") != "" || o.ClientID("other") != "" || o.CallbackHost != "127.0.0.1" {
		t.Fatalf("%+v", o)
	}
	if !o.BackgroundRefresh || o.RefreshDelay != 1500*time.Millisecond {
		t.Fatalf("refresh settings: %+v", o)
	}
	off, err := LoadOAuth(oauthConfig(map[string]any{"oauth.background_refresh": false, "oauth.refresh_delay": "0s"}))
	if err != nil || off.BackgroundRefresh || off.RefreshDelay != 0 {
		t.Fatalf("switched off: %+v %v", off, err)
	}
	var none *OAuth
	if none.ClientID("claude") != "" {
		t.Fatal("a nil view answered")
	}
}

func TestOAuthSettingsRefuseBadValuesWithoutRepeatingThem(t *testing.T) {
	for name, over := range map[string]map[string]any{
		"host not an address": {"oauth.callback_host": "evil.example"},
		"empty host":          {"oauth.callback_host": ""},
		"zoned host":          {"oauth.callback_host": "fe80::1%eth0"},
		"id with a space":     {"oauth.codex_client_id": "a b"},
		"id with a newline":   {"oauth.codex_client_id": "a\nb"},
		"id too long":         {"oauth.claude_client_id": strings.Repeat("x", maxClientIDLen+1)},
	} {
		_, err := LoadOAuth(oauthConfig(over))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		for _, v := range over {
			if s, _ := v.(string); len(s) > 3 && strings.Contains(err.Error(), s) {
				t.Errorf("%s: the error repeats the value: %v", name, err)
			}
		}
	}
	for _, bad := range []string{"61s", "-1s", "soon"} {
		o, err := LoadOAuth(oauthConfig(map[string]any{"oauth.refresh_delay": bad}))
		if err != nil {
			t.Errorf("refresh delay %q stopped the start: %v", bad, err)
			continue
		}
		if o.RefreshDelay != 1500*time.Millisecond || len(o.Warnings) != 1 || !strings.Contains(o.Warnings[0], "OAUTH_REFRESH_DELAY") {
			t.Errorf("refresh delay %q: %v %v", bad, o.RefreshDelay, o.Warnings)
		}
	}
}
