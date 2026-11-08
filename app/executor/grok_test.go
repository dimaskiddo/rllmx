package executor

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"rllmx/tests"
)

const nativeFC = "fc_01234567-89ab-cdef-0123-456789abcdef"
const nativeRS = "rs_01234567-89ab-cdef-0123-456789abcdef"

func grokIn(input string) string {
	return `{"model":"grok-4.5","stream":false,"store":true,"instructions":"s","temperature":0.2,"user":"u","max_tokens":5,"prompt_cache_key":"client","input":` + input + `}`
}

func grokReq(body string, mod func(*Request)) Request {
	r := Request{Body: []byte(body), Session: "ks", Levels: []string{"low", "medium", "high", "xhigh"}}
	if mod != nil {
		mod(&r)
	}
	return r
}

func TestGrokKeepsOnlyItsFieldsAndAlwaysStreams(t *testing.T) {
	out := shapeGrok(grokReq(grokIn(`[{"type":"message","role":"user","content":"hi"}]`), nil))
	gjson.ParseBytes(out).ForEach(func(k, _ gjson.Result) bool {
		if !grokFields[k.String()] {
			t.Errorf("field %q escaped", k.String())
		}
		return true
	})
	if !gjson.GetBytes(out, "stream").Bool() || gjson.GetBytes(out, "store").Bool() {
		t.Fatalf("%s", out)
	}
	if gjson.GetBytes(out, "prompt_cache_key").String() != "ks" || gjson.GetBytes(out, "user").Exists() || gjson.GetBytes(out, "max_tokens").Exists() {
		t.Fatalf("%s", out)
	}
	if gjson.GetBytes(out, "temperature").Float() != 0.2 {
		t.Fatalf("temperature is allowed on this backend: %s", out)
	}
}

func TestGrokKeepsTheSystemRoleAsItIs(t *testing.T) {
	out := shapeGrok(grokReq(grokIn(`[{"type":"message","role":"system","content":"s"},{"type":"message","role":"user","content":"hi"}]`), nil))
	if gjson.GetBytes(out, "input.0.role").String() != "system" {
		t.Fatalf("%s", gjson.GetBytes(out, "input").Raw)
	}
}

func TestGrokInputItems(t *testing.T) {
	in := `[
	 {"type":"message","role":"user","content":"hi"},
	 {"type":"reasoning","id":"` + nativeRS + `","encrypted_content":"blob"},
	 {"type":"reasoning","id":"rs_other","encrypted_content":"blob"},
	 {"type":"reasoning","id":"` + nativeRS + `"},
	 {"type":"custom_tool_call","call_id":"c1","name":"patch","input":"diff"},
	 {"type":"function_call","id":"` + nativeFC + `","call_id":"c2","name":"read","arguments":"{}"},
	 {"type":"function_call","id":"fc_server","call_id":"c3","name":"read","arguments":{"a":1}},
	 {"type":"function_call_output","call_id":"c1","output":{"ok":true}},
	 {"type":"function_call_output","call_id":"ghost","output":"x"},
	 {"type":"item_reference","id":"msg_1"},
	 {"type":"function_call","call_id":"","name":"x"}]`
	out := shapeGrok(grokReq(grokIn(in), nil))
	items := gjson.GetBytes(out, "input").Array()
	var kinds []string
	for _, it := range items {
		kinds = append(kinds, it.Get("type").String())
	}
	if got := strings.Join(kinds, ","); got != "message,reasoning,function_call,function_call,function_call,function_call_output" {
		t.Fatalf("kinds %s\n%s", got, gjson.GetBytes(out, "input").Raw)
	}
	custom := items[2]
	if custom.Get("call_id").String() != "c1" || gjson.Get(custom.Get("arguments").String(), "input").String() != "diff" {
		t.Fatalf("custom call not turned into a function call: %s", custom.Raw)
	}
	if items[3].Get("id").String() != nativeFC || items[4].Get("id").Exists() {
		t.Fatalf("item ids: %s / %s", items[3].Raw, items[4].Raw)
	}
	if items[4].Get("arguments").String() != `{"a":1}` {
		t.Fatalf("object arguments: %s", items[4].Raw)
	}
	if items[5].Get("output").String() != `{"ok":true}` {
		t.Fatalf("output: %s", items[5].Raw)
	}
}

func TestGrokEffortIsSentOnlyForAModelThatDeclaresLevels(t *testing.T) {
	body := grokIn(`[{"type":"message","role":"user","content":"hi"}]`)
	body = strings.Replace(body, `"store":true,`, `"store":true,"reasoning":{"effort":"max"},`, 1)
	with := shapeGrok(grokReq(body, nil))
	if gjson.GetBytes(with, "reasoning.effort").String() != "xhigh" || gjson.GetBytes(with, "reasoning.summary").String() != "concise" {
		t.Fatalf("%s", gjson.GetBytes(with, "reasoning").Raw)
	}
	if gjson.GetBytes(with, "include.0").String() != "reasoning.encrypted_content" {
		t.Fatalf("include: %s", with)
	}
	without := shapeGrok(grokReq(body, func(r *Request) { r.Levels = nil }))
	if gjson.GetBytes(without, "reasoning.effort").Exists() || gjson.GetBytes(without, "reasoning.summary").String() != "concise" {
		t.Fatalf("effort sent to a model without levels: %s", gjson.GetBytes(without, "reasoning").Raw)
	}
	dflt := shapeGrok(grokReq(grokIn(`[]`), nil))
	if gjson.GetBytes(dflt, "reasoning.effort").String() != "high" {
		t.Fatalf("default effort: %s", gjson.GetBytes(dflt, "reasoning").Raw)
	}
}

func TestGrokToolsAndForcedChoice(t *testing.T) {
	body := `{"model":"m","input":[],"tools":[{"type":"function","function":{"name":"a","parameters":{"type":"object"}}},{"type":"x_search"},{"type":"custom","name":"free"}],"tool_choice":{"type":"function","function":{"name":"a"},"name":"a"}}`
	out := shapeGrok(grokReq(body, nil))
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 3 || tools[1].Get("type").String() != "x_search" || gjson.Get(tools[2].Get("parameters.properties.input.type").Raw, "@this").String() != "string" {
		t.Fatalf("%s", gjson.GetBytes(out, "tools").Raw)
	}
	if gjson.GetBytes(out, "tool_choice.type").String() != "function" || gjson.GetBytes(out, "tool_choice.name").String() != "a" {
		t.Fatalf("%s", gjson.GetBytes(out, "tool_choice").Raw)
	}
	ghost := shapeGrok(grokReq(strings.Replace(body, `"name":"a"},"name":"a"`, `"name":"ghost"},"name":"ghost"`, 1), nil))
	if gjson.GetBytes(ghost, "tool_choice").Exists() {
		t.Fatalf("a choice naming no tool stayed: %s", ghost)
	}
	none := shapeGrok(grokReq(`{"model":"m","input":[],"tools":[{"type":"mystery"}],"tool_choice":"auto"}`, nil))
	if gjson.GetBytes(none, "tools").Exists() || gjson.GetBytes(none, "tool_choice").Exists() {
		t.Fatalf("empty tools left behind: %s", none)
	}
}

func TestGrokTurnIndexIsMonotonicAndStableAcrossAFailover(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := &Grok{Now: func() time.Time { return now }}
	turn1 := []byte(grokIn(`[{"type":"message","role":"user","content":"a"}]`))
	turn2 := []byte(grokIn(`[{"type":"message","role":"user","content":"a"},{"type":"message","role":"assistant","content":"x"},{"type":"message","role":"user","content":"b"}]`))
	delta := []byte(grokIn(`[{"type":"message","role":"user","content":"c"}]`))
	if got := g.turnIndex("s", turn1); got != 1 {
		t.Fatalf("first turn %d", got)
	}
	if got := g.turnIndex("s", turn1); got != 1 {
		t.Fatalf("a retry of the same request advanced the turn to %d", got)
	}
	if got := g.turnIndex("s", turn2); got != 2 {
		t.Fatalf("second turn %d", got)
	}
	if got := g.turnIndex("s", delta); got != 3 {
		t.Fatalf("a delta request after turn 2 must be turn 3, got %d", got)
	}
	if got := g.turnIndex("other", delta); got != 1 {
		t.Fatalf("another session started at %d", got)
	}
	now = now.Add(3 * time.Hour)
	if got := g.turnIndex("s", delta); got != 1 {
		t.Fatalf("an expired session kept its count: %d", got)
	}
}

func TestGrokTurnStoreIsBoundedAndPerExecutor(t *testing.T) {
	g := &Grok{}
	body := []byte(grokIn(`[{"type":"message","role":"user","content":"a"}]`))
	for i := 0; i < grokMaxTurns+50; i++ {
		g.turnIndex(strings.Repeat("x", 3)+time.Duration(i).String(), body)
	}
	if len(g.turns) > grokMaxTurns {
		t.Fatalf("%d sessions kept", len(g.turns))
	}
	other := &Grok{}
	if other.turnIndex("x0s", body) != 1 || len(other.turns) != 1 {
		t.Fatal("two executors share a counter")
	}
}

func TestGrokSendsFreshHeadersOnEveryCall(t *testing.T) {
	up := tests.NewUpstream(t, tests.Reply{ContentType: "text/event-stream", Body: okStream})
	g := &Grok{Default: &Default{Client: http.DefaultClient}}
	acct := func(k string) string {
		return map[string]string{"email": "a@x.test", "userId": "u1", "connectionId": "conn-1"}[k]
	}
	for i := 0; i < 2; i++ {
		req := Request{URL: up.URL + "/responses", Key: secret, Auth: bearerAuth(), Session: "ks", Account: acct, Levels: []string{"low"},
			Body: []byte(grokIn(`[{"type":"message","role":"user","content":"hi"}]`))}
		resp, err := g.Execute(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	calls := up.Calls()
	h := calls[0].Header
	for k, want := range map[string]string{
		"X-Grok-Session-Id": "ks", "X-Grok-Conv-Id": "ks", "X-Grok-Turn-Idx": "1", "X-Grok-Model-Override": "grok-4.5",
		"X-Email": "a@x.test", "X-Userid": "u1", "Authorization": "Bearer " + secret,
	} {
		if h.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, h.Get(k), want)
		}
	}
	if h.Get("X-Grok-Req-Id") == "" || h.Get("X-Grok-Req-Id") == calls[1].Header.Get("X-Grok-Req-Id") {
		t.Errorf("request ids %q %q", h.Get("X-Grok-Req-Id"), calls[1].Header.Get("X-Grok-Req-Id"))
	}
	if h.Get("X-Grok-Agent-Id") == "" || h.Get("X-Grok-Agent-Id") != calls[1].Header.Get("X-Grok-Agent-Id") {
		t.Errorf("the agent id must be stable for an account: %q %q", h.Get("X-Grok-Agent-Id"), calls[1].Header.Get("X-Grok-Agent-Id"))
	}
	if calls[1].Header.Get("X-Grok-Turn-Idx") != "1" {
		t.Errorf("the same request sent twice advanced the turn: %s", calls[1].Header.Get("X-Grok-Turn-Idx"))
	}
}

func TestGrokAgentIDPrefersTheStoredOneAndNeverUsesTheMachine(t *testing.T) {
	stored := grokAgent(Request{Account: func(k string) string { return map[string]string{"deviceId": "dev-9"}[k] }})
	if stored != "dev-9" {
		t.Fatalf("stored id ignored: %s", stored)
	}
	a := grokAgent(Request{Account: func(k string) string { return map[string]string{"connectionId": "c1"}[k] }})
	b := grokAgent(Request{Account: func(k string) string { return map[string]string{"connectionId": "c2"}[k] }})
	if a == b || len(a) != 36 {
		t.Fatalf("derived ids %q %q", a, b)
	}
}

func TestGrokHeadersRefuseAControlCharacter(t *testing.T) {
	g := &Grok{}
	h := g.headers(nil, Request{Session: "s", Account: func(k string) string {
		return map[string]string{"email": "a\r\nX-Evil: 1", "userId": "ok"}[k]
	}}, []byte(grokIn(`[]`)))
	if _, bad := h["x-email"]; bad || h["x-userid"] != "ok" {
		t.Fatalf("%v", h)
	}
}

func TestGrokNoKeyIsRefused(t *testing.T) {
	g := &Grok{Default: &Default{Client: http.DefaultClient}}
	if _, err := g.Execute(context.Background(), Request{URL: "http://x.invalid"}); err != ErrNoCredential {
		t.Fatalf("%v", err)
	}
}
