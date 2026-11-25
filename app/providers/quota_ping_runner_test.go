package providers

import (
	"testing"
	"time"

	"rllmx/app/llm/quota"
)

func TestAWindowIsWarmedOnlyAfterItsReset(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	reading := quota.Reading{Quotas: map[string]quota.Quota{
		"session (5h)": {ResetAt: &past},
		"weekly":       {ResetAt: &future},
	}}
	key, ok := dueWindow(reading, "claude", now)
	if !ok || key != past.Format(time.RFC3339) {
		t.Fatalf("due %q %v", key, ok)
	}
	if _, ok := dueWindow(reading, "codex", now); ok {
		t.Fatal("a Claude window was read as Codex's")
	}
	if _, ok := dueWindow(quota.Reading{Quotas: map[string]quota.Quota{"session (5h)": {ResetAt: &future}}}, "claude", now); ok {
		t.Fatal("a window that has not reset was warmed")
	}
}

func TestOnlyClaudeAndCodexAreWarmed(t *testing.T) {
	if pingSwitch("claude") != "claudeAutoPing" || pingSwitch("codex") != "codexAutoPing" {
		t.Fatal("the switches are not the settings the page writes")
	}
	if pingSwitch("openai") != "" {
		t.Fatal("a provider without a reset window is warmed")
	}
}

func TestAnAutoPingSettingIsReadInBothShapes(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		id   string
		want bool
	}{
		{`true`, "c1", true},
		{`false`, "c1", false},
		{``, "c1", false},
		{`{"connections":{"c1":true,"c2":false}}`, "c1", true},
		{`{"connections":{"c1":true,"c2":false}}`, "c2", false},
		{`{"connections":{"c1":true}}`, "other", false},
		{`{"schedules":{"c1":{"enabled":true}}}`, "c1", false},
	} {
		if got := pingWanted(tc.raw, tc.id); got != tc.want {
			t.Errorf("%q for %s: %v, want %v", tc.raw, tc.id, got, tc.want)
		}
	}
}
