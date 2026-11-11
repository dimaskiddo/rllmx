package opencode

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"rllmx/app/llm/executor"
	"rllmx/app/llm/upstream"
	"rllmx/tests"
	"rllmx/tests/llm"
)

func openCodeFreeReq(up *tests.Upstream, model string) executor.Request {
	return executor.Request{URL: up.URL, Body: []byte(`{"model":"` + model + `","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`),
		Model: model, Session: "conv-1"}
}

func TestOpenCodePicksTheLaneByModel(t *testing.T) {
	lane := openCodeLane{responses: "https://x/responses", messages: "https://x/messages", chat: "https://x/chat"}
	for model, want := range map[string]string{
		"muse-spark-1.3-contributor-free": "https://x/responses",
		"union-alpha":                     "https://x/messages",
		"union-alpha(high)":               "https://x/messages",
		"glm-5":                           "https://x/chat",
	} {
		if got := openCodeURL(lane, model); got != want {
			t.Errorf("%s: %s, want %s", model, got, want)
		}
	}
}

func TestOpenCodeAFreeCallCarriesTheGateIdentity(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	o := &OpenCodeFree{Default: &executor.Default{Client: up.Server.Client()},
		Lane: openCodeLane{chat: up.URL + "/inference/v1/chat/completions"}}
	if _, err := o.Execute(context.Background(), openCodeFreeReq(up, "glm-5")); err != nil {
		t.Fatalf("%v", err)
	}
	h := up.Calls()[0].Header
	if got := h.Get("Authorization"); got != "Bearer public" {
		t.Errorf("authorization %q", got)
	}
	if got := h.Get("User-Agent"); !strings.HasPrefix(got, "opencode/latest/2.0.26/") {
		t.Errorf("user agent %q", got)
	}
	session := h.Get("x-opencode-session")
	if session == "" || session != h.Get("x-opencode-session-id") || session != h.Get("x-session-id") || session != h.Get("x-session-affinity") {
		t.Errorf("the four session headers differ: %q", session)
	}
	if got := h.Get("x-opencode-project"); got != "global" {
		t.Errorf("project %q", got)
	}
}

func TestOpenCodeAFreeBodyCarriesTheGateToolsAndMarker(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	o := &OpenCodeFree{Default: &executor.Default{Client: up.Server.Client()}, Lane: openCodeLane{chat: up.URL + "/chat"}}
	req := openCodeFreeReq(up, "glm-5")
	req.Body = []byte(`{"model":"glm-5","stream":false,"messages":[{"role":"user","content":"hi"}]}`)
	if _, err := o.Execute(context.Background(), req); err != nil {
		t.Fatalf("%v", err)
	}
	body := up.Calls()[0].Body
	if !gjson.GetBytes(body, "stream").Bool() {
		t.Errorf("the request was not forced to stream: %s", body)
	}
	names := map[string]bool{}
	for _, tool := range gjson.GetBytes(body, "tools").Array() {
		names[tool.Get("function.name").String()] = true
		if def := openCodeFingerprintToolFor(tool.Get("function.name").String()); def.name != "" {
			if tool.Get("function.description").String() != def.description {
				t.Errorf("%s does not carry the gate's definition", def.name)
			}
		}
	}
	if !names["read"] || !names["shell"] {
		t.Errorf("the gate tools are missing: %s", gjson.GetBytes(body, "tools").Raw)
	}
	var system strings.Builder
	for _, m := range gjson.GetBytes(body, "messages").Array() {
		if m.Get("role").String() == "system" {
			system.WriteString(m.Get("content").String())
		}
	}
	joined := system.String()
	if !strings.Contains(joined, openCodeEnvMarker) || !strings.Contains(joined, "placeholders that do nothing") {
		t.Errorf("the system turns are %q", joined)
	}
	if strings.Contains(joined, "/home/") || strings.Contains(joined, "linux/") {
		t.Errorf("the host reached the prompt: %q", joined)
	}
	// The marker's values are fixed words, never the host's own.
	if !strings.Contains(joined, "Platform: linux") || !strings.Contains(joined, "Working directory: /workspace") {
		t.Errorf("the marker does not carry the fixed values: %q", joined)
	}
}

func TestOpenCodeACallerToolOnAGateNameMovesAside(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"read","description":"mine","parameters":{"type":"object","properties":{"url":{"type":"string"}}}}}],"messages":[{"role":"user","content":"hi"}]}`)
	out := openCodeFingerprint(body, false)
	renames := openCodeRenames(out)
	if renames["read_client"] != "read" {
		t.Fatalf("no alias was recorded: %v", renames)
	}
	names := map[string]bool{}
	for _, tool := range gjson.GetBytes(out, "tools").Array() {
		names[tool.Get("function.name").String()] = true
	}
	if !names["read_client"] || !names["read"] || !names["shell"] {
		t.Fatalf("tools %v", names)
	}
	if strings.Contains(string(openCodeWithoutRenames(out)), openCodeRenamed) {
		t.Fatal("the rename map would have reached the wire")
	}
}

func TestOpenCodeACallerToolWithTheSameArgumentsAdoptsTheDefinition(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","function":{"name":"read","description":"mine","parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],"messages":[{"role":"user","content":"hi"}]}`)
	out := openCodeFingerprint(body, false)
	if renames := openCodeRenames(out); len(renames) != 0 {
		t.Fatalf("the tool moved aside: %v", renames)
	}
	read := gjson.GetBytes(out, `tools.#(function.name=="read")`)
	if read.Get("function.description").String() != openCodeFingerprintToolFor("read").description {
		t.Errorf("the caller's wording survived: %s", read.Raw)
	}
}

func TestOpenCodeTheResponsesLaneRewritesTheBody(t *testing.T) {
	body := []byte(`{"input":"","max_tokens":10,"reasoning_effort":"ultra","store":true,"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`)
	out := openCodeResponsesBody(body, []string{"low", "xhigh", "max"})
	if !gjson.GetBytes(out, "input").IsArray() {
		t.Errorf("input %s", gjson.GetBytes(out, "input").Raw)
	}
	if got := gjson.GetBytes(out, "max_output_tokens").Int(); got != 10 {
		t.Errorf("max_output_tokens %d", got)
	}
	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Error("max_tokens survived")
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "max" {
		t.Errorf("effort %q, want the nearest level the model declares", got)
	}
	if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "auto" {
		t.Errorf("summary %q", got)
	}
	if gjson.GetBytes(out, "store").Bool() {
		t.Error("store stayed on")
	}
	if !gjson.GetBytes(out, "tools.0.parameters.properties").Exists() {
		t.Errorf("the tool is not flat: %s", gjson.GetBytes(out, "tools.0").Raw)
	}
}

func TestOpenCodeKeyedSendsTheKeyAndAFreeIdIsGated(t *testing.T) {
	for name, tc := range map[string]struct {
		model  string
		header string
		want   string
	}{
		"a paid id": {"deepseek-v4-pro", "Authorization", "Bearer k"},
		"a free id": {"deepseek-v4-flash-free", "Authorization", "Bearer public"},
	} {
		up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
		o := &OpenCodeKeyed{Default: &executor.Default{Client: up.Server.Client()}, Zen: true}
		req := executor.Request{URL: up.URL, Key: "k", Model: tc.model, Body: []byte(`{"model":"` + tc.model + `"}`),
			Auth: llm.Bearer(), Entry: upstream.Entry{Transport: upstream.Transport{Format: upstream.FormatOpenAI}}}
		if _, err := o.Execute(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := up.Calls()[0].Header.Get(tc.header); got != tc.want {
			t.Errorf("%s: %s %q, want %q", name, tc.header, got, tc.want)
		}
	}
}

func TestOpenCodeZenAddsItsStubTools(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{Body: "{}"})
	o := &OpenCodeKeyed{Default: &executor.Default{Client: up.Server.Client()}, Zen: true}
	req := executor.Request{URL: up.URL, Key: "k", Model: "glm-5", Body: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
		Auth: llm.Bearer()}
	if _, err := o.Execute(context.Background(), req); err != nil {
		t.Fatalf("%v", err)
	}
	names := map[string]bool{}
	for _, tool := range gjson.GetBytes(up.Calls()[0].Body, "tools").Array() {
		names[tool.Get("function.name").String()] = true
	}
	for _, want := range []string{"bash", "glob", "grep", "read"} {
		if !names[want] {
			t.Errorf("%s missing: %v", want, names)
		}
	}
	if got := up.Calls()[0].Header.Get("User-Agent"); got != "opencode/1.18.31" {
		t.Errorf("user agent %q", got)
	}
}

func TestOpenCodeATerminalToolGoesOutUnderAnotherName(t *testing.T) {
	body := []byte(`{"tools":[
		{"type":"function","function":{"name":"terminal","parameters":{"type":"object"}}},
		{"type":"function","function":{"name":"my-Terminal-2","parameters":{"type":"object"}}}],
		"messages":[{"role":"user","content":"hi"}]}`)
	out := openCodeFingerprint(body, false)
	renames := openCodeRenames(out)
	if renames["run_command"] != "terminal" || renames["run_command2"] != "my-Terminal-2" {
		t.Fatalf("aliases = %v", renames)
	}
	var names []string
	for _, tool := range gjson.GetBytes(openCodeWithoutRenames(out), "tools").Array() {
		names = append(names, tool.Get("function.name").String())
	}
	if strings.Contains(strings.Join(names, ","), "terminal") || strings.Contains(strings.Join(names, ","), "Terminal") {
		t.Fatalf("a terminal name reached the wire: %v", names)
	}
}
