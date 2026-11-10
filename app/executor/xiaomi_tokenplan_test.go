package executor

import (
	"context"
	"testing"

	"rllmx/app/upstream"
	"rllmx/tests"
)

func TestTokenPlanAddressFollowsTheClusterAndTheLane(t *testing.T) {
	for name, tc := range map[string]struct {
		region, format, want string
	}{
		"the default cluster": {"", upstream.FormatOpenAI, "https://token-plan-sgp.xiaomimimo.com/v1/chat/completions"},
		"singapore":           {"sgp", upstream.FormatOpenAI, "https://token-plan-sgp.xiaomimimo.com/v1/chat/completions"},
		"china":               {"cn", upstream.FormatOpenAI, "https://token-plan-cn.xiaomimimo.com/v1/chat/completions"},
		"amsterdam":           {"ams", upstream.FormatClaude, "https://token-plan-ams.xiaomimimo.com/anthropic/v1/messages"},
		"an unknown cluster falls back to the default": {"mars", upstream.FormatOpenAI, "https://token-plan-sgp.xiaomimimo.com/v1/chat/completions"},
	} {
		got := tokenPlanAddress(acct(map[string]string{"region": tc.region}), tc.format)
		if got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestTokenPlanAClaudeModelGoesToTheMessagesLane(t *testing.T) {
	for name, tc := range map[string]struct {
		entry  upstream.Entry
		spec   upstream.Model
		format string
	}{
		"the provider reads the client's format": {
			upstream.Entry{Transport: upstream.Transport{Format: upstream.FormatClaude}}, upstream.Model{}, "claude",
		},
		"a model's own target format wins": {
			upstream.Entry{Transport: upstream.Transport{Format: upstream.FormatOpenAI}},
			upstream.Model{TargetFormat: upstream.FormatClaude}, "claude",
		},
		"a plain model stays on chat": {
			upstream.Entry{Transport: upstream.Transport{Format: upstream.FormatOpenAI}}, upstream.Model{}, "openai",
		},
	} {
		if got := tokenPlanFormat(Request{Entry: tc.entry, Spec: tc.spec}); got != tc.format {
			t.Errorf("%s: %q, want %q", name, got, tc.format)
		}
	}
}

func TestTokenPlanSendsWithNoKey(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: `{}`})
	x := &XiaomiTokenPlan{Default: &Default{Client: up.Server.Client()}}
	if _, err := x.Execute(context.Background(), Request{URL: up.URL, Body: []byte(`{}`)}); err == nil {
		t.Fatal("a request with no key was sent")
	}
	if len(up.Calls()) != 0 {
		t.Fatalf("%d calls with no key", len(up.Calls()))
	}
}
