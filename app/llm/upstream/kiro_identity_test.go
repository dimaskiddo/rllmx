package upstream

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func machineOf(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

func TestTheKiroIdentityIsTheIDEsCurrentOneAndNamesTheClientItCallsThrough(t *testing.T) {
	h := KiroIdentity(KiroSeed{ClientID: "client-1"}, KiroStreaming)
	id := "KiroIDE-" + KiroIDEVersion + "-" + machineOf("client-1")
	want := "aws-sdk-js/1.0.0 ua/2.1 os/windows#10.0.26200 lang/js md/nodejs#22.21.1 api/codewhispererstreaming#1.0.0 m/E " + id
	if h["User-Agent"] != want {
		t.Fatalf("user agent\n got %q\nwant %q", h["User-Agent"], want)
	}
	if h["x-amz-user-agent"] != "aws-sdk-js/1.0.0 "+id {
		t.Fatalf("x-amz-user-agent %q", h["x-amz-user-agent"])
	}
	runtime := KiroIdentity(KiroSeed{ClientID: "client-1"}, KiroRuntime)
	if !strings.Contains(runtime["User-Agent"], "api/codewhispererruntime#1.0.0 m/N,E ") {
		t.Fatalf("runtime client: %q", runtime["User-Agent"])
	}
}

func TestTheKiroMachineIDComesFromTheMostStableIdentifier(t *testing.T) {
	for name, tc := range map[string]struct {
		seed KiroSeed
		want string
	}{
		"client id wins":      {KiroSeed{ClientID: "c", RefreshToken: "r", ProfileARN: "p", APIKey: "k", AccessToken: "a"}, "c"},
		"then refresh token":  {KiroSeed{RefreshToken: "r", ProfileARN: "p", APIKey: "k", AccessToken: "a"}, "r"},
		"then profile":        {KiroSeed{ProfileARN: "p", APIKey: "k", AccessToken: "a"}, "p"},
		"then api key":        {KiroSeed{APIKey: "k", AccessToken: "a"}, "k"},
		"then access token":   {KiroSeed{AccessToken: "a"}, "a"},
		"otherwise anonymous": {KiroSeed{}, "kiro-anonymous"},
	} {
		got := KiroIdentity(tc.seed, KiroRuntime)["x-amz-user-agent"]
		if !strings.HasSuffix(got, machineOf(tc.want)) {
			t.Errorf("%s: %q does not end in the hash of %q", name, got, tc.want)
		}
	}
}

func TestARotatingAccessTokenKeepsTheSameKiroIdentityAndNoCredentialIsInIt(t *testing.T) {
	a := KiroIdentity(KiroSeed{RefreshToken: "refresh-1", AccessToken: "access-A"}, KiroStreaming)
	b := KiroIdentity(KiroSeed{RefreshToken: "refresh-1", AccessToken: "access-B"}, KiroStreaming)
	if a["User-Agent"] != b["User-Agent"] {
		t.Fatal("the identity changed when only the access token rotated")
	}
	for _, v := range a {
		if strings.Contains(v, "refresh-1") || strings.Contains(v, "access-A") {
			t.Fatalf("a credential reached a header: %q", v)
		}
	}
	if KiroIdentity(KiroSeed{RefreshToken: "refresh-2"}, KiroStreaming)["User-Agent"] == a["User-Agent"] {
		t.Fatal("two accounts share an identity")
	}
}

func TestAKiroDataFileCarriesNoIdentityOfItsOwn(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"kiro", "kiro-social"} {
		e, ok := reg.ByID(id)
		if !ok {
			t.Fatalf("no %s entry", id)
		}
		for name, v := range e.Transport.Headers {
			if strings.Contains(strings.ToLower(name), "user-agent") || strings.Contains(v, "kiro-ide/1.0.0") {
				t.Errorf("%s still names an old identity in %s: %q", id, name, v)
			}
		}
	}
}

// The Antigravity IDE identity is the one a live client sends, so a data file
// left on an older build is refused or rate-limited as an unknown client.
func TestAntigravityPresentsTheCurrentHubBuildAndOffersItsNewTiers(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := reg.ByID("antigravity")
	if !ok {
		t.Fatal("no antigravity entry")
	}
	const ua = "antigravity/hub/2.33.0 darwin/arm64"
	if e.Transport.Headers["User-Agent"] != ua || e.OAuth.CallHeaders["User-Agent"] != ua {
		t.Fatalf("user agents %q / %q", e.Transport.Headers["User-Agent"], e.OAuth.CallHeaders["User-Agent"])
	}
	for id, want := range map[string]string{
		"gemini-4-hydrogen-xhigh": "gemini-4-hydrogen-xhigh(xhigh)",
		"gemini-4-hydrogen-high":  "gemini-4-hydrogen-high(high)",
		"gemini-4-hydrogen":       "gemini-4-hydrogen-high(high)",
		"gemini-4-neon-xhigh":     "gemini-4-neon-xhigh(xhigh)",
		"gemini-4-neon":           "gemini-4-neon-high(high)",
		"gemini-4-argon":          "gemini-4-argon-high(high)",
		"gemini-4-carbon":         "gemini-4-carbon-high(high)",
		"claude-sonnet-5-5-max":   "claude-sonnet-5-5-max(max)",
		"claude-opus-5-5-max":     "claude-opus-5-5-max(max)",
		"claude-sonnet-5-5":       "claude-sonnet-5-5-medium(medium)",
		"claude-opus-5-5":         "claude-opus-5-5-medium(medium)",
	} {
		if got := reg.WireModel("antigravity", id); got != want {
			t.Errorf("%s goes upstream as %q, want %q", id, got, want)
		}
	}
}

// GPT 6.2 is refused by a Codex client older than 0.166.0, and the extended
// context forms are the same model under another name.
func TestCodexOffersGPT62AndItsExtendedContextForms(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		"gpt-6.2-sol":       "gpt-6.2-sol",
		"gpt-6.2-sol[1m]":   "gpt-6.2-sol",
		"gpt-6.2-astra":     "gpt-6.2-astra",
		"gpt-6.2-astra[1m]": "gpt-6.2-astra",
	} {
		if got := reg.WireModel("codex", id); got != want {
			t.Errorf("%s goes upstream as %q, want %q", id, got, want)
		}
	}
	sol, _ := reg.Model("codex", "gpt-6.2-sol")
	if !sol.Lite || len(sol.Levels) != 5 {
		t.Errorf("gpt-6.2-sol: %+v", sol)
	}
	if astra, _ := reg.Model("codex", "gpt-6.2-astra"); astra.Lite {
		t.Error("astra is not a lite model")
	}
	e, _ := reg.ByID("codex")
	if v := e.OAuth.Identity["version"]; v < "0.166.0" {
		t.Errorf("the Codex identity %q is older than GPT 6.2 accepts", v)
	}
	if _, ok := reg.Model("kimi", "k3.1-pro"); !ok {
		t.Error("kimi has no k3.1-pro")
	}
	if _, ok := reg.Model("kimi", "k3.1-pro-highspeed"); !ok {
		t.Error("kimi has no k3.1-pro-highspeed")
	}
}

// Alibaba's token plan serves an Anthropic endpoint beside its OpenAI one, so a
// Claude client is sent there without a translation, and it lists the newer
// Qwen and DeepSeek builds.
func TestAlibabaTokenPlanOffersAClaudeLaneAndItsNewModels(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	e, ok := reg.ByID("alitp-intl")
	if !ok {
		t.Fatal("no alitp-intl entry")
	}
	lane, ok := e.Transport.Lanes["claude"]
	if !ok || lane.BaseURL != "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1/messages" {
		t.Fatalf("claude lane %+v", lane)
	}
	if !strings.Contains(e.Transport.BaseURL, "compatible-mode/v1/chat/completions") {
		t.Errorf("the OpenAI endpoint is no longer the default: %q", e.Transport.BaseURL)
	}
	for _, id := range []string{"auto", "qwen4-max", "qwen4-max-preview", "qwen3.8-max", "qwen3.8-flash", "glm-5.3", "deepseek-v4-pro-0813", "deepseek-v4-flash-0731", "deepseek-v4.1-flash", "deepseek-v4.1-flash-1017"} {
		if _, ok := reg.Model("alitp-intl", id); !ok {
			t.Errorf("alitp-intl does not list %s", id)
		}
	}
}
