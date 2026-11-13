package account

import (
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

func TestCooldownDoublesAndIsCapped(t *testing.T) {
	for level, want := range map[int]time.Duration{
		-3: 2 * time.Second, 0: 2 * time.Second, 1: 2 * time.Second, 2: 4 * time.Second, 3: 8 * time.Second,
		7: 128 * time.Second, 8: 256 * time.Second, 9: 5 * time.Minute, 15: 5 * time.Minute, 16: 5 * time.Minute, 1 << 30: 5 * time.Minute,
	} {
		if got := Cooldown(level); got != want {
			t.Errorf("Cooldown(%d) = %v, want %v", level, got, want)
		}
	}
}

func TestNextLevelIsCappedAndNeverNegative(t *testing.T) {
	for level, want := range map[int]int{-5: 1, 0: 1, 1: 2, 14: 15, 15: 15, 99: 15} {
		if got := NextLevel(level); got != want {
			t.Errorf("NextLevel(%d) = %d, want %d", level, got, want)
		}
	}
}

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		name     string
		in       Input
		fallback bool
		scoped   bool
		cooldown time.Duration
		next     int
	}{
		{"429 backs off", Input{Status: 429, Level: 0}, true, false, 2 * time.Second, 1},
		{"429 at level 3", Input{Status: 429, Level: 3}, true, false, 16 * time.Second, 4},
		{"429 at the cap", Input{Status: 429, Level: 15}, true, false, 5 * time.Minute, 15},
		{"401 is two minutes and keeps the level", Input{Status: 401, Level: 5}, true, false, 2 * time.Minute, 5},
		{"402", Input{Status: 402}, true, false, 2 * time.Minute, 0},
		{"403", Input{Status: 403}, true, false, 2 * time.Minute, 0},
		{"404", Input{Status: 404}, true, false, 2 * time.Minute, 0},
		{"500 is transient", Input{Status: 500, Level: 2}, true, false, 30 * time.Second, 2},
		{"network error (status 0)", Input{Status: 0}, true, false, 30 * time.Second, 0},
		{"400 is request scoped", Input{Status: 400}, false, true, 0, 0},
		{"406, 408, 413", Input{Status: 413}, false, true, 0, 0},
		{"rate limit text on a 500", Input{Status: 500, Text: "Rate Limit hit"}, true, false, 2 * time.Second, 1},
		{"rate limit text on a 403 backs off", Input{Status: 403, Text: "rate limit"}, true, false, 2 * time.Second, 1},
		{"rate limit text on a 400 stays scoped", Input{Status: 400, Text: "please explain rate limit"}, false, true, 0, 0},
		{"overloaded on a 422 stays scoped", Input{Status: 422, Text: "overloaded"}, false, true, 0, 0},
		{"insufficient credit is not a Limit rule", Input{Status: 400, Text: "insufficient credit"}, true, false, 2 * time.Second, 1},
		{"no credentials", Input{Status: 400, Text: "No credentials"}, true, false, 2 * time.Minute, 0},
		{"request not allowed", Input{Status: 400, Text: "request not allowed"}, true, false, 5 * time.Second, 0},
		{"text beats status", Input{Status: 401, Text: "request not allowed"}, true, false, 5 * time.Second, 0},
		{"codex chatgpt model text", Input{Provider: "codex", Status: 400, Text: "Model is not supported when using Codex with a ChatGPT account"}, true, false, 30 * time.Minute, 0},
		{"codex text for another provider", Input{Provider: "openai", Status: 400, Text: "model is not supported when using codex with a chatgpt account"}, false, true, 0, 0},
		{"codebuddy-intl request illegal", Input{Provider: "codebuddy-intl", Status: 400, Text: "request illegal"}, true, false, 2 * time.Second, 1},
		{"request illegal elsewhere", Input{Provider: "x", Status: 400, Text: "request illegal"}, false, true, 0, 0},
		{"nvidia degraded cluster", Input{Provider: "nvidia", Status: 400, Text: "the model cluster is degraded"}, true, false, 15 * time.Minute, 0},
		{"degraded elsewhere", Input{Provider: "x", Status: 400, Text: "degraded"}, false, true, 0, 0},
	}
	for _, c := range cases {
		c.in.Now = t0
		d := Classify(c.in)
		if d.Fallback != c.fallback || d.RequestScoped != c.scoped || d.Cooldown != c.cooldown || d.NextLevel != c.next {
			t.Errorf("%s: %+v", c.name, d)
		}
	}
}

func TestOnlyTheFirst4096BytesAreScanned(t *testing.T) {
	late := strings.Repeat("x", maxScanned) + " rate limit"
	if d := Classify(Input{Status: 500, Text: late, Now: t0}); d.Cooldown != 30*time.Second {
		t.Fatalf("text past the scan window matched: %+v", d)
	}
	early := "rate limit " + strings.Repeat("x", maxScanned)
	if d := Classify(Input{Status: 500, Text: early, Now: t0}); d.Cooldown != 2*time.Second {
		t.Fatalf("%+v", d)
	}
}

func TestAReportedResetReplacesTheCooldownOnlyWhenFuture(t *testing.T) {
	future := Classify(Input{Status: 429, Level: 4, ResetAt: t0.Add(10 * time.Minute), Now: t0})
	if future.Cooldown != 10*time.Minute || future.NextLevel != 0 || !future.Fallback {
		t.Fatalf("%+v", future)
	}
	for name, at := range map[string]time.Time{"past": t0.Add(-time.Minute), "now": t0, "zero": {}} {
		d := Classify(Input{Status: 429, Level: 4, ResetAt: at, Now: t0})
		if d.Cooldown != Cooldown(5) || d.NextLevel != 5 {
			t.Errorf("%s reset changed the lock: %+v", name, d)
		}
	}
}

func TestAReportedResetNeverLocksARequestScopedError(t *testing.T) {
	d := Classify(Input{Status: 400, ResetAt: t0.Add(time.Hour), Now: t0})
	if d.Fallback || !d.RequestScoped || d.Cooldown != 0 {
		t.Fatalf("%+v", d)
	}
}

func TestAReportedResetIsCappedExceptForAntigravity(t *testing.T) {
	far := t0.Add(5 * time.Hour)
	if d := Classify(Input{Provider: "codex", Status: 429, ResetAt: far, Now: t0}); d.Cooldown != 30*time.Minute {
		t.Errorf("codex %v", d.Cooldown)
	}
	if d := Classify(Input{Provider: "antigravity", Status: 429, ResetAt: far, Now: t0}); d.Cooldown != 5*time.Hour {
		t.Errorf("antigravity %v", d.Cooldown)
	}
}

func TestGitHubMonthlyLimitLocksUntilTheNextUTCMonth(t *testing.T) {
	now := time.Date(2026, 3, 31, 23, 0, 0, 0, time.UTC)
	d := Classify(Input{Provider: "github", Status: 402, Text: "You've reached your additional usage limit for your plan", Now: now})
	if !d.Fallback || !d.AccountWide || d.Cooldown != time.Hour || d.NextLevel != 0 {
		t.Fatalf("%+v", d)
	}
	dec := Classify(Input{Provider: "github", Status: 402, Text: "You've reached your additional usage limit for your plan", Now: time.Date(2026, 12, 15, 9, 0, 0, 0, time.UTC)})
	if want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC).Sub(time.Date(2026, 12, 15, 9, 0, 0, 0, time.UTC)); dec.Cooldown != want {
		t.Fatalf("december rollover: %v want %v", dec.Cooldown, want)
	}
	other := Classify(Input{Provider: "openai", Status: 402, Text: "you've reached your additional usage limit for your plan", Now: now})
	if other.AccountWide {
		t.Fatal("the GitHub rule applied to another provider")
	}
	nine := Classify(Input{Provider: "github", Status: 429, Text: "you've reached your additional usage limit for your plan", Now: now})
	if nine.AccountWide {
		t.Fatal("the GitHub rule applied to a status other than 402")
	}
}
