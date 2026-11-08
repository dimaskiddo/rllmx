package controllers

import (
	"context"
	"strings"
	"testing"

	"rllmx/app/upstream"
)

func nodeID(t *testing.T, out map[string]any) string {
	t.Helper()
	n, _ := out["node"].(map[string]any)
	id, _ := n["id"].(string)
	if id == "" {
		t.Fatalf("no node id in %v", out)
	}
	return id
}

func TestNodeCreateBuildsTheIDAndStoresCleanFields(t *testing.T) {
	m := newMgmtRig(t)
	cases := map[string]struct {
		body, idPrefix, wantBase string
	}{
		"openai chat":      {`{"name":"A","prefix":"aa","apiType":"chat","baseUrl":"https://llm.example.com/v1/"}`, upstream.NodeOpenAIPrefix + "chat-", "https://llm.example.com/v1"},
		"openai responses": {`{"name":"B","prefix":"qq","apiType":"responses","baseUrl":"https://r.example.com/v1"}`, upstream.NodeOpenAIPrefix + "responses-", "https://r.example.com/v1"},
		"anthropic":        {`{"name":"C","prefix":"cc1","type":"anthropic-compatible","baseUrl":"https://a.example.com/v1/messages"}`, upstream.NodeAnthropicPrefix, "https://a.example.com/v1"},
		"embedding":        {`{"name":"D","prefix":"dd","type":"custom-embedding","baseUrl":"https://e.example.com/v1/embeddings/"}`, upstream.NodeEmbeddingPrefix, "https://e.example.com/v1"},
		"default base url": {`{"name":"E","prefix":"ee","apiType":"chat"}`, upstream.NodeOpenAIPrefix + "chat-", "https://api.openai.com/v1"},
	}
	for name, c := range cases {
		out := m.ok(t, "POST", "/api/provider-nodes", c.body, 201)
		n := out["node"].(map[string]any)
		if id := n["id"].(string); !strings.HasPrefix(id, c.idPrefix) {
			t.Errorf("%s: id %q", name, id)
		}
		if n["baseUrl"] != c.wantBase {
			t.Errorf("%s: baseUrl %v, want %s", name, n["baseUrl"], c.wantBase)
		}
	}
	if got := m.ok(t, "GET", "/api/provider-nodes", "", 200)["nodes"].([]any); len(got) != len(cases) {
		t.Fatalf("%d nodes listed", len(got))
	}
}

func TestNodeCreateValidation(t *testing.T) {
	m := newMgmtRig(t)
	ok := `"name":"N","apiType":"chat","baseUrl":"https://x.example.com/v1"`
	bad := map[string]string{
		"no name":             `{"prefix":"p1","apiType":"chat"}`,
		"no prefix":           `{"name":"N","apiType":"chat"}`,
		"bad api type":        `{"name":"N","prefix":"p1","apiType":"soap"}`,
		"no api type":         `{"name":"N","prefix":"p1"}`,
		"bad node type":       `{"name":"N","prefix":"p1","type":"mystery"}`,
		"upper case prefix":   `{` + ok + `,"prefix":"Pfx"}`,
		"prefix with a slash": `{` + ok + `,"prefix":"a/b"}`,
		"prefix with a space": `{` + ok + `,"prefix":"a b"}`,
		"prefix too long":     `{` + ok + `,"prefix":"` + strings.Repeat("a", 33) + `"}`,
		"leading dash":        `{` + ok + `,"prefix":"-a"}`,
		"built-in id":         `{` + ok + `,"prefix":"openai"}`,
		"built-in alias":      `{` + ok + `,"prefix":"cc"}`,
		"extra alias":         `{` + ok + `,"prefix":"gb"}`,
		"local name":          `{` + ok + `,"prefix":"polly"}`,
		"file scheme":         `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"file:///etc/passwd"}`,
		"ftp scheme":          `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"ftp://x.example.com"}`,
		"no host":             `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"https://"}`,
		"credentials":         `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"https://u:p@x.example.com"}`,
		"query":               `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"https://x.example.com/v1?key=1"}`,
		"fragment":            `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"https://x.example.com/v1#a"}`,
		"control in url":      "{\"name\":\"N\",\"prefix\":\"p1\",\"apiType\":\"chat\",\"baseUrl\":\"https://x.example.com/\\u0085\"}",
		"url too long":        `{"name":"N","prefix":"p1","apiType":"chat","baseUrl":"https://x.example.com/` + strings.Repeat("a", 2100) + `"}`,
		"control in name":     "{\"name\":\"a\\u0085b\",\"prefix\":\"p1\",\"apiType\":\"chat\"}",
	}
	for name, body := range bad {
		if code, _, raw := m.call("POST", "/api/provider-nodes", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
	if nodes := m.ok(t, "GET", "/api/provider-nodes", "", 200)["nodes"].([]any); len(nodes) != 0 {
		t.Fatalf("a refused create stored %d nodes", len(nodes))
	}
}

func TestAHTTPLoopbackNodeIsAllowed(t *testing.T) {
	// The dial guard decides about private addresses at connect time; refusing
	// them here would break a local Ollama node.
	m := newMgmtRig(t)
	m.ok(t, "POST", "/api/provider-nodes", `{"name":"local","prefix":"loc","apiType":"chat","baseUrl":"http://127.0.0.1:11434/v1"}`, 201)
}

func TestNodePrefixesAreUnique(t *testing.T) {
	m := newMgmtRig(t)
	m.ok(t, "POST", "/api/provider-nodes", `{"name":"A","prefix":"same","apiType":"chat"}`, 201)
	if code, _, raw := m.call("POST", "/api/provider-nodes", `{"name":"B","prefix":"same","type":"anthropic-compatible"}`, true); code != 409 {
		t.Fatalf("%d %s", code, raw)
	}
	other := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"B","prefix":"other","apiType":"chat"}`, 201))
	if code, _, raw := m.call("PUT", "/api/provider-nodes/"+other, `{"name":"B","prefix":"same","apiType":"chat","baseUrl":"https://x.example.com/v1"}`, true); code != 409 {
		t.Fatalf("rename onto a taken prefix: %d %s", code, raw)
	}
}

func TestAConnectionOnANodeCopiesTheNodeAndIgnoresTheClient(t *testing.T) {
	m := newMgmtRig(t)
	node := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"Mine","prefix":"mine","apiType":"chat","baseUrl":"https://real.example.com/v1"}`, 201))
	out := m.ok(t, "POST", "/api/providers",
		`{"provider":"`+node+`","apiKey":"k","name":"conn","providerSpecificData":{"baseUrl":"https://evil.example.com","prefix":"cc","extra":"kept?"}}`, 201)
	id := connID(t, out)
	c, _, _ := m.st.Connections().Get(context.Background(), id)
	raw, _ := c.Doc().GetRaw("providerSpecificData")
	for _, want := range []string{`"baseUrl":"https://real.example.com/v1"`, `"prefix":"mine"`, `"nodeName":"Mine"`, `"apiType":"chat"`} {
		if !strings.Contains(raw, want) {
			t.Errorf("%s missing from %s", want, raw)
		}
	}
	if strings.Contains(raw, "evil") || strings.Contains(raw, "extra") {
		t.Fatalf("a client-sent value reached a node connection: %s", raw)
	}
	if code, _, _ := m.call("POST", "/api/providers", `{"provider":"openai-compatible-chat-nope","apiKey":"k"}`, true); code != 404 {
		t.Errorf("a missing node: %d", code)
	}
	for _, f := range []string{"baseUrl", "prefix", "apiType", "nodeName"} {
		if code, _, _ := m.call("PUT", "/api/providers/"+id, `{"`+f+`":"x"}`, true); code != 400 {
			t.Errorf("top-level %s accepted on update: %d", f, code)
		}
		if code, _, _ := m.call("PUT", "/api/providers/"+id, `{"providerSpecificData":{"`+f+`":"x"}}`, true); code != 400 {
			t.Errorf("providerSpecificData.%s accepted on a node connection: %d", f, code)
		}
	}
}

func TestRenamingANodeUpdatesItsConnectionsInOneStep(t *testing.T) {
	m := newMgmtRig(t)
	node := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"Old","prefix":"old","apiType":"chat","baseUrl":"https://a.example.com/v1"}`, 201))
	c1 := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"`+node+`","apiKey":"k1","name":"one"}`, 201))
	c2 := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"`+node+`","apiKey":"k2","name":"two"}`, 201))
	m.ok(t, "PUT", "/api/provider-nodes/"+node, `{"name":"New","prefix":"new","apiType":"responses","baseUrl":"https://b.example.com/v1"}`, 200)
	for _, id := range []string{c1, c2} {
		c, _, _ := m.st.Connections().Get(context.Background(), id)
		raw, _ := c.Doc().GetRaw("providerSpecificData")
		for _, want := range []string{`"prefix":"new"`, `"nodeName":"New"`, `"apiType":"responses"`, `"baseUrl":"https://b.example.com/v1"`} {
			if !strings.Contains(raw, want) {
				t.Errorf("%s: %s missing from %s", id, want, raw)
			}
		}
		if strings.Contains(raw, "old") || strings.Contains(raw, "a.example.com") {
			t.Errorf("%s kept a stale copy: %s", id, raw)
		}
	}
	if code, _, _ := m.call("PUT", "/api/provider-nodes/"+node, `{"name":"X","prefix":"new","apiType":"chat"}`, true); code != 400 {
		t.Errorf("a missing base URL on update: %d", code)
	}
	if code, _, _ := m.call("PUT", "/api/provider-nodes/nope", `{"name":"X","prefix":"x1","apiType":"chat","baseUrl":"https://x.example.com"}`, true); code != 404 {
		t.Errorf("an unknown node: %d", code)
	}
}

func TestDeletingANodeTakesItsConnectionsAndNothingElse(t *testing.T) {
	m := newMgmtRig(t)
	ctx := context.Background()
	node := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"N","prefix":"nn","apiType":"chat"}`, 201))
	other := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"O","prefix":"oo","apiType":"chat"}`, 201))
	gone := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"`+node+`","apiKey":"k","name":"x"}`, 201))
	kept := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"`+other+`","apiKey":"k","name":"y"}`, 201))
	plain := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"z"}`, 201))
	if err := m.st.KV().Set(ctx, "customModels", node+"|m1|llm", `{"id":"m1"}`); err != nil {
		t.Fatal(err)
	}
	if err := m.st.KV().Set(ctx, "customModels", other+"|m2|llm", `{"id":"m2"}`); err != nil {
		t.Fatal(err)
	}
	m.ok(t, "DELETE", "/api/provider-nodes/"+node, "", 200)
	if _, ok, _ := m.st.Connections().Get(ctx, gone); ok {
		t.Error("the node's connection survived")
	}
	for _, id := range []string{kept, plain} {
		if _, ok, _ := m.st.Connections().Get(ctx, id); !ok {
			t.Errorf("connection %s was deleted with another node", id)
		}
	}
	models, _ := m.st.KV().List(ctx, "customModels")
	if _, ok := models[node+"|m1|llm"]; ok {
		t.Error("the node's custom model survived")
	}
	if _, ok := models[other+"|m2|llm"]; !ok {
		t.Error("another node's custom model was deleted")
	}
	if code, _, _ := m.call("DELETE", "/api/provider-nodes/"+node, "", true); code != 404 {
		t.Errorf("second delete: %d", code)
	}
}

func TestANodeConnectionListsUnderTheNodesNameWhenUnnamed(t *testing.T) {
	m := newMgmtRig(t)
	node := nodeID(t, m.ok(t, "POST", "/api/provider-nodes", `{"name":"Fancy Node","prefix":"fn","apiType":"chat"}`, 201))
	m.ok(t, "POST", "/api/providers", `{"provider":"`+node+`","apiKey":"k"}`, 201)
	_, _, raw := m.call("GET", "/api/providers", "", true)
	if !strings.Contains(raw, "Fancy Node") {
		t.Fatalf("%s", raw)
	}
}
