package controllers

import (
	"context"
	"strings"
	"testing"
)

func TestBulkAddCreatesOnePerLineAndReportsEach(t *testing.T) {
	m := newMgmtRig(t)
	body := `{"provider":"deepseek-web","entries":"first|sso=abc\nsso=xyz\nbad line with space"}`
	out := m.ok(t, "POST", "/api/providers/bulk", body, 200)
	results, _ := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results = %v", out["results"])
	}
	first, _ := results[0].(map[string]any)
	if first["connectionId"] == nil || first["name"] != "first" {
		t.Fatalf("first = %v", first)
	}
	second, _ := results[1].(map[string]any)
	if second["connectionId"] == nil || second["name"] != "Key 2" {
		t.Fatalf("second = %v", second)
	}
	third, _ := results[2].(map[string]any)
	if third["error"] == nil {
		t.Fatalf("the malformed line was accepted: %v", third)
	}
}

func TestBulkAddRefusesAListPastTheCap(t *testing.T) {
	m := newMgmtRig(t)
	lines := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		lines = append(lines, "sso="+strings.Repeat("a", 4))
	}
	code, _, raw := m.call("POST", "/api/providers/bulk", `{"provider":"deepseek-web","entries":`+quoteJSON(strings.Join(lines, "\n"))+`}`, true)
	if code != 400 {
		t.Fatalf("status = %d: %s", code, raw)
	}
}

func TestACookieConnectionRotatesItsSecret(t *testing.T) {
	m := newMgmtRig(t)
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"deepseek-web","apiKey":"sso=old","name":"c1"}`, 201))
	before, found, err := m.st.Connections().Get(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("get: %v %v", err, found)
	}
	oldKey, _ := before.Doc().GetString("apiKey")
	if code, _, raw := m.call("PUT", "/api/providers/"+id, `{"apiKey":"bad value"}`, true); code != 400 {
		t.Fatalf("a malformed rotation was accepted: %d %s", code, raw)
	}
	m.ok(t, "PUT", "/api/providers/"+id, `{"apiKey":"sso=new"}`, 200)
	after, _, err := m.st.Connections().Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	newKey, _ := after.Doc().GetString("apiKey")
	if newKey == "" || newKey == oldKey {
		t.Fatalf("the secret did not rotate: %q -> %q", oldKey, newKey)
	}
}

func TestBulkAddKeepsALlmKeyOpaque(t *testing.T) {
	m := newMgmtRig(t)
	body := `{"provider":"deepseek-web","entries":"llm|acct-1|secret-1\nLLM|acct-2|secret-2\nnamed|plain-key"}`
	out := m.ok(t, "POST", "/api/providers/bulk", body, 200)
	results, _ := out["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("results = %v", out["results"])
	}
	first, _ := results[0].(map[string]any)
	if first["name"] != "LLM 1" || first["connectionId"] == nil {
		t.Fatalf("first = %v", first)
	}
	second, _ := results[1].(map[string]any)
	if second["name"] != "LLM 2" {
		t.Fatalf("second = %v", second)
	}
	third, _ := results[2].(map[string]any)
	if third["name"] != "named" {
		t.Fatalf("third = %v", third)
	}
	// The opaque key is stored whole, not split at any pipe.
	conn, found, err := m.st.Connections().Get(context.Background(), first["connectionId"].(string))
	if err != nil || !found {
		t.Fatalf("get: %v %v", err, found)
	}
	key, _ := conn.Doc().GetString("apiKey")
	if key == "" || key == "acct-1|secret-1" {
		t.Fatalf("the key was split: %q", key)
	}
}
