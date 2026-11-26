package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const cloakBody = `{"model":"claude-opus-5","max_tokens":10,"system":"be brief","messages":[` +
	`{"role":"user","content":"hi"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read_file","input":{}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}],` +
	`"tools":[{"name":"read_file","description":"d","input_schema":{"type":"object"}},` +
	`{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"read_file"}}`

func disguise() *Disguise {
	return &Disguise{Seed: "sk-ant-oat01-abc", Session: "claude:11111111-2222-3333-4444-555555555555", Tools: true}
}

func TestTheBillingBlockComesFirstAndHashesTheBodyBeforeAnyRename(t *testing.T) {
	out, _ := Cloak([]byte(cloakBody), disguise())
	first := gjson.GetBytes(out, "system.0.text").String()
	re := regexp.MustCompile(`^x-anthropic-billing-header: cc_version=2\.1\.322\.[0-9a-f]{3}; cc_entrypoint=sdk-cli; cch=([0-9a-f]{5});$`)
	m := re.FindStringSubmatch(first)
	if m == nil {
		t.Fatalf("billing block = %q", first)
	}
	sum := sha256.Sum256([]byte(cloakBody))
	if m[1] != hex.EncodeToString(sum[:])[:5] {
		t.Fatalf("cch = %s: it must hash the body as it was before the block, the user id and the renames", m[1])
	}
	if gjson.GetBytes(out, "system.1.text").String() != "be brief" || len(gjson.GetBytes(out, "system").Array()) != 2 {
		t.Fatalf("the client's system prompt was lost: %s", gjson.GetBytes(out, "system").Raw)
	}
}

func TestTheBillingBlockJoinsAnArraySystemAndNoSystem(t *testing.T) {
	arr, _ := Cloak([]byte(`{"system":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}],"messages":[]}`), &Disguise{Seed: "t"})
	if gjson.GetBytes(arr, "system.#").Int() != 2 || gjson.GetBytes(arr, "system.1.cache_control.type").String() != "ephemeral" {
		t.Fatalf("%s", arr)
	}
	none, _ := Cloak([]byte(`{"messages":[]}`), &Disguise{Seed: "t"})
	if gjson.GetBytes(none, "system.#").Int() != 1 || !strings.HasPrefix(gjson.GetBytes(none, "system.0.text").String(), billingPrefix) {
		t.Fatalf("%s", none)
	}
}

func TestAnExistingBillingBlockIsNotInjectedTwice(t *testing.T) {
	once, _ := Cloak([]byte(`{"system":"s","messages":[]}`), &Disguise{Seed: "t"})
	twice, _ := Cloak(once, &Disguise{Seed: "t"})
	if gjson.GetBytes(twice, "system.#").Int() != 2 {
		t.Fatalf("%s", twice)
	}
}

const clientBilling = "x-anthropic-billing-header: cc_version=2.1.322.abc; cc_entrypoint=sdk-cli; cch=12345;"

func TestBillingOfReadsTheLineOutOfAStringOrAnArray(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"array", `{"system":[{"type":"text","text":"` + clientBilling + `"},{"type":"text","text":"rest"}]}`, clientBilling},
		{"string", `{"system":"` + clientBilling + `\nrest"}`, clientBilling},
		{"another first block", `{"system":[{"type":"text","text":"rest"}]}`, ""},
		{"no system", `{"messages":[]}`, ""},
		{"a line without the prefix", `{"system":"hello"}`, ""},
	} {
		if got := BillingOf([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestWithBillingLinePutsTheClientsOwnLineBack(t *testing.T) {
	out := WithBillingLine([]byte(`{"system":[{"type":"text","text":"You are Claude Code."}],"messages":[]}`), clientBilling)
	if gjson.GetBytes(out, "system.0.text").String() != clientBilling || gjson.GetBytes(out, "system.1.text").String() != "You are Claude Code." {
		t.Fatalf("%s", out)
	}
	if again := WithBillingLine(out, clientBilling); gjson.GetBytes(again, "system.#").Int() != 2 {
		t.Fatalf("the line was added twice: %s", again)
	}
	if got := string(WithBillingLine([]byte(`{"messages":[]}`), "hello")); got != `{"messages":[]}` {
		t.Fatalf("a line without the prefix changed the body: %s", got)
	}
}

func TestTheUserIDIsClaudeCodesAndStablePerAccount(t *testing.T) {
	out, _ := Cloak([]byte(cloakBody), disguise())
	uid := gjson.GetBytes(out, "metadata.user_id").String()
	device := sha256.Sum256([]byte("device:sk-ant-oat01-abc"))
	if gjson.Get(uid, "device_id").String() != hex.EncodeToString(device[:]) {
		t.Fatalf("device id: %s", uid)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(gjson.Get(uid, "account_uuid").String()) {
		t.Fatalf("account uuid: %s", uid)
	}
	if gjson.Get(uid, "session_id").String() != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("the claude: prefix must go: %s", uid)
	}
	order := regexp.MustCompile(`^\{"device_id":"[0-9a-f]{64}","account_uuid":"[^"]+","session_id":"[^"]+"\}$`)
	if !order.MatchString(uid) {
		t.Fatalf("key order: %s", uid)
	}
	again, _ := Cloak([]byte(cloakBody), disguise())
	if gjson.GetBytes(again, "metadata.user_id").String() != uid {
		t.Fatal("the user id changed between two requests of one account and session")
	}
	other, _ := Cloak([]byte(cloakBody), &Disguise{Seed: "sk-ant-oat01-other", Session: "s", Tools: true})
	if gjson.Get(gjson.GetBytes(other, "metadata.user_id").String(), "device_id").String() == gjson.Get(uid, "device_id").String() {
		t.Fatal("two accounts share a device id")
	}
}

func TestAClientsOwnUserIDIsKept(t *testing.T) {
	out, _ := Cloak([]byte(`{"metadata":{"user_id":"mine","x":1},"messages":[]}`), &Disguise{Seed: "t"})
	if gjson.GetBytes(out, "metadata.user_id").String() != "mine" || gjson.GetBytes(out, "metadata.x").Int() != 1 {
		t.Fatalf("%s", out)
	}
}

func TestASessionThatCouldBreakTheUserIDGetsAFreshOne(t *testing.T) {
	for _, bad := range []string{"", `a"b`, `a\b`, "a\nb"} {
		out, _ := Cloak([]byte(`{"messages":[]}`), &Disguise{Seed: "t", Session: bad})
		if !gjson.Valid(gjson.GetBytes(out, "metadata.user_id").String()) {
			t.Errorf("session %q broke the user id: %s", bad, out)
		}
	}
}

func TestToolsAreRenamedExceptTypedOnesAndTheHistoryAndChoiceFollow(t *testing.T) {
	out, names := Cloak([]byte(cloakBody), disguise())
	if gjson.GetBytes(out, "tools.0.name").String() != "read_file_ide" || gjson.GetBytes(out, "tools.0.description").String() != "d" {
		t.Fatalf("%s", gjson.GetBytes(out, "tools.0").Raw)
	}
	if gjson.GetBytes(out, "tools.1.name").String() != "web_search" || gjson.GetBytes(out, "tools.1.type").String() != "web_search_20250305" {
		t.Fatalf("a typed tool must keep its reserved name: %s", gjson.GetBytes(out, "tools.1").Raw)
	}
	if gjson.GetBytes(out, "messages.1.content.0.name").String() != "read_file_ide" {
		t.Fatalf("history: %s", gjson.GetBytes(out, "messages.1").Raw)
	}
	if gjson.GetBytes(out, "tool_choice.name").String() != "read_file_ide" {
		t.Fatalf("tool_choice: %s", gjson.GetBytes(out, "tool_choice").Raw)
	}
	if len(names) != 1 || names["read_file_ide"] != "read_file" {
		t.Fatalf("names = %v", names)
	}
}

func TestAToolChoiceForATypedToolIsLeftAlone(t *testing.T) {
	body := `{"messages":[],"tools":[{"name":"mine","input_schema":{}},{"type":"web_search_20250305","name":"web_search"}],"tool_choice":{"type":"tool","name":"web_search"}}`
	out, _ := Cloak([]byte(body), disguise())
	if gjson.GetBytes(out, "tool_choice.name").String() != "web_search" {
		t.Fatalf("%s", gjson.GetBytes(out, "tool_choice").Raw)
	}
}

func TestTwentyDecoysFollowTheClientTools(t *testing.T) {
	out, _ := Cloak([]byte(cloakBody), disguise())
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 2+20 {
		t.Fatalf("%d tools", len(tools))
	}
	for i, want := range decoyNames {
		d := tools[2+i]
		if d.Get("name").String() != want || d.Get("description").String() != "This tool is currently unavailable." ||
			d.Get("input_schema.type").String() != "object" || len(d.Get("input_schema.properties").Map()) != 0 {
			t.Errorf("decoy %d = %s", i, d.Raw)
		}
	}
}

func TestABodyWithNoToolsGetsNeitherRenamesNorDecoys(t *testing.T) {
	out, names := Cloak([]byte(`{"messages":[{"role":"user","content":"hi"}]}`), disguise())
	if gjson.GetBytes(out, "tools").Exists() || names != nil {
		t.Fatalf("%s %v", out, names)
	}
	if !strings.HasPrefix(gjson.GetBytes(out, "system.0.text").String(), billingPrefix) || gjson.GetBytes(out, "metadata.user_id").String() == "" {
		t.Fatalf("the billing block and user id still apply: %s", out)
	}
}

func TestOnlyTheBillingHalfAppliesWhenToolsAreOff(t *testing.T) {
	d := disguise()
	d.Tools = false
	out, names := Cloak([]byte(cloakBody), d)
	if gjson.GetBytes(out, "tools.0.name").String() != "read_file" || len(gjson.GetBytes(out, "tools").Array()) != 2 || names != nil {
		t.Fatalf("%s", gjson.GetBytes(out, "tools").Raw)
	}
	if gjson.GetBytes(out, "system.0.text").String() == "" {
		t.Fatal("no billing block")
	}
}

func TestAClientToolAlreadyNamedLikeADecoyOrWithTheSuffixRoundTrips(t *testing.T) {
	body := `{"messages":[],"tools":[{"name":"Bash","input_schema":{}},{"name":"x_ide","input_schema":{}}]}`
	out, names := Cloak([]byte(body), disguise())
	if gjson.GetBytes(out, "tools.0.name").String() != "Bash_ide" || gjson.GetBytes(out, "tools.1.name").String() != "x_ide_ide" {
		t.Fatalf("%s", gjson.GetBytes(out, "tools").Raw)
	}
	if Uncloak("Bash_ide", names, true) != "Bash" || Uncloak("x_ide_ide", names, true) != "x_ide" {
		t.Fatalf("the map must decide: %v", names)
	}
	if Uncloak("Bash", names, true) != "Bash" {
		t.Fatal("the decoy Bash came back changed")
	}
}

// A decoy name the map lost must reach the client as it is: stripping the
// suffix would turn "unavailable" into a silent call of a real tool.
func TestAMappedDecoyNameIsLeftAloneWithoutTheMap(t *testing.T) {
	if got := Uncloak("Read_ide", map[string]string{}, true); got != "Read_ide" {
		t.Fatalf("a lost decoy became %q", got)
	}
	// A tool the client really named like a decoy is renamed like any other, and
	// the map brings it back (TestUncloak covers the direction).
	if got := Uncloak("Read_ide", map[string]string{"Read_ide": "Read"}, true); got != "Read" {
		t.Fatalf("a client tool the map renamed became %q", got)
	}
}

func TestUncloak(t *testing.T) {
	names := map[string]string{"read_file_ide": "read_file"}
	cases := []struct {
		name    string
		in      string
		cloaked bool
		want    string
	}{
		{"by the map", "read_file_ide", true, "read_file"},
		{"by the suffix when the map lost it", "other_ide", true, "other"},
		{"a decoy is never touched", "Task", true, "Task"},
		{"an unmapped name that strips back to a decoy is left alone", "Task_ide", true, "Task_ide"},
		{"no suffix", "plain", true, "plain"},
		{"only the suffix", "_ide", true, "_ide"},
		{"the suffix is dropped even when the attempt was not cloaked", "x_ide", false, "x"},
		{"the suffix is dropped without the map too", "read_file_ide", false, "read_file"},
		{"a decoy is left alone", "Task", false, "Task"},
		{"a decoy is left alone even with the suffix", "Bash_ide", false, "Bash_ide"},
	}
	for _, tc := range cases {
		if got := Uncloak(tc.in, names, tc.cloaked); got != tc.want {
			t.Errorf("%s: %q -> %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

func TestACloakedBodyKeepsEveryOtherByteOfTheClientsRequest(t *testing.T) {
	body := `{"model":"m","max_tokens":5,"temperature":0.5,"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}],"tools":[{"name":"a","input_schema":{"type":"object","properties":{"x":{"type":"string"}}},"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`
	out, _ := Cloak([]byte(body), disguise())
	for _, path := range []string{"model", "max_tokens", "temperature", "messages", "tools.0.input_schema", "tools.0.cache_control"} {
		if gjson.GetBytes(out, path).Raw != gjson.Get(body, path).Raw {
			t.Errorf("%s changed: %s vs %s", path, gjson.GetBytes(out, path).Raw, gjson.Get(body, path).Raw)
		}
	}
}

func TestTwoTurnsOfAToolLoopStayByteIdenticalApartFromTheBillingBlock(t *testing.T) {
	turn1 := `{"model":"m","messages":[{"role":"user","content":"go"}],"tools":[{"name":"a","input_schema":{}}]}`
	turn2 := `{"model":"m","messages":[{"role":"user","content":"go"},{"role":"assistant","content":[{"type":"text","text":"ok"}]}],"tools":[{"name":"a","input_schema":{}}]}`
	a, _ := Cloak([]byte(turn1), disguise())
	b, _ := Cloak([]byte(turn2), disguise())
	if gjson.GetBytes(a, "tools").Raw != gjson.GetBytes(b, "tools").Raw {
		t.Fatal("the tool list changed between two turns, which would cost the upstream's prompt cache")
	}
	if gjson.GetBytes(a, "metadata").Raw != gjson.GetBytes(b, "metadata").Raw {
		t.Fatal("the user id changed between two turns")
	}
}

func TestANonObjectBodyComesBackAsItWas(t *testing.T) {
	for _, in := range []string{``, `[]`, `"x"`, `nope`} {
		out, names := Cloak([]byte(in), disguise())
		if string(out) != in || names != nil {
			t.Errorf("%q -> %q", in, out)
		}
	}
	if out, _ := Cloak([]byte(`{}`), nil); string(out) != `{}` {
		t.Fatal("a nil disguise changed the body")
	}
}

func TestSessionOf(t *testing.T) {
	for in, want := range map[string]string{
		`{"device_id":"d","session_id":"claude:abc"}`: "abc",
		`{"session_id":"abc"}`:                        "abc",
		"claude:xyz":                                  "xyz",
		"plain":                                       "plain",
		"":                                            "",
		`{"nope":1}`:                                  "",
	} {
		if got := SessionOf(in); got != want {
			t.Errorf("SessionOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzCloak(f *testing.F) {
	for _, s := range []string{cloakBody, `{}`, `{"tools":[]}`, `{"tools":[{"name":"a"}]}`, `{"system":1}`, `{"messages":[{"content":[{"type":"tool_use"}]}]}`, `[]`, ``} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, names := Cloak([]byte(s), disguise())
		if !gjson.ValidBytes([]byte(s)) || !gjson.Parse(s).IsObject() {
			if string(out) != s {
				t.Fatalf("a non-object body changed: %q", s)
			}
			return
		}
		if !gjson.ValidBytes(out) {
			t.Fatalf("invalid JSON out for %q: %s", s, out)
		}
		in := gjson.Get(s, "tools")
		if in.IsArray() && len(in.Array()) > 0 {
			if got := len(gjson.GetBytes(out, "tools").Array()); got != len(in.Array())+20 {
				t.Fatalf("%d tools out for %d in", got, len(in.Array()))
			}
		}
		for suffixed, orig := range names {
			if Uncloak(suffixed, names, true) != orig {
				t.Fatalf("%q does not come back as %q", suffixed, orig)
			}
		}
	})
}

func FuzzUncloak(f *testing.F) {
	for _, s := range []string{"a_ide", "Task", "_ide", "", "x_ide_ide", "Task_ide"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		// The reference drops the suffix whatever the attempt: only a decoy and a
		// name that is the suffix alone stay.
		got := Uncloak(name, nil, false)
		if got != name && !(!decoySet[name] && strings.HasSuffix(name, toolSuffix) && len(name) > len(toolSuffix)) {
			t.Fatalf("%q became %q", name, got)
		}
		got = Uncloak(name, nil, true)
		if decoySet[name] && got != name {
			t.Fatalf("decoy %q changed to %q", name, got)
		}
		if len(got) > len(name) {
			t.Fatalf("%q grew to %q", name, got)
		}
	})
}

func TestTheDisguiseAddsNoCacheMarkerSoTheUpstreamLimitOfFourHolds(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}],` +
		`"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"tools":[{"name":"a","input_schema":{},"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`
	before := strings.Count(body, "cache_control")
	out, _ := Cloak([]byte(body), disguise())
	if after := strings.Count(string(out), "cache_control"); after != before {
		t.Fatalf("%d markers before, %d after", before, after)
	}
}

func TestTheBillingVersionIsTheOneTheRouterAdvertises(t *testing.T) {
	d := disguise()
	d.Version = "9.9.9"
	out, _ := Cloak([]byte(cloakBody), d)
	if got := gjson.GetBytes(out, "system.0.text").String(); !strings.Contains(got, "cc_version=9.9.9.") {
		t.Fatalf("%q", got)
	}
}
