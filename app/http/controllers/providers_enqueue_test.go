package controllers

import (
	"context"
	"strings"
	"sync"
	"testing"

	catalogguard "rllmx/app/llm/catalog"
	"rllmx/app/store/repository"
)

// enqueued records the connection ids a route asked to probe.
type enqueued struct {
	mu  sync.Mutex
	ids []string
}

func (e *enqueued) add(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.ids = append(e.ids, id)
}

func (e *enqueued) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ids...)
}

func TestAChangedConnectionAsksForAProbe(t *testing.T) {
	m := newMgmtRig(t)
	e := &enqueued{}
	m.pc.Enqueue = e.add

	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201))
	if got := e.all(); len(got) != 1 || got[0] != id {
		t.Fatalf("create enqueued %v, want [%s]", got, id)
	}

	m.ok(t, "PUT", "/api/providers/"+id, `{"name":"b"}`, 200)
	if got := e.all(); len(got) != 2 || got[1] != id {
		t.Fatalf("update enqueued %v", got)
	}
}

func TestABulkImportDoesNotEnqueueEveryConnection(t *testing.T) {
	m := newMgmtRig(t)
	e := &enqueued{}
	m.pc.Enqueue = e.add
	m.ok(t, "POST", "/api/providers/bulk",
		`{"provider":"deepseek-web","entries":"first|sso=abc\nsecond|sso=xyz"}`, 200)
	if got := e.all(); len(got) != 0 {
		t.Fatalf("a bulk import enqueued %v", got)
	}
}

// A key test checks the credential and spends nothing on models; model
// reachability is the model-test route.
func TestAKeyCheckDoesNotProbeEveryModel(t *testing.T) {
	m := newMgmtRig(t)
	e := &enqueued{}
	m.pc.Enqueue = e.add
	id := connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201))
	// The create itself asks for a probe; the test must add none.
	m.pc.Enqueue = e.add
	before := len(e.all())
	m.ok(t, "POST", "/api/providers/"+id+"/test", "", 200)
	if got := e.all(); len(got) != before {
		t.Fatalf("a key test enqueued %v", got[before:])
	}
}

// An OAuth provider's model page answers its own live list, with the embedded
// list as the fallback.
func TestTheModelListUsesTheProvidersLiveCatalog(t *testing.T) {
	m := newMgmtRig(t)
	conn, err := m.st.Store.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "kiro", AuthType: "oauth", Name: "k",
		Fields: map[string]string{"accessToken": `"at-` + strings.Repeat("x", 60) + `"`},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.pc.LiveCatalog = &catalogguard.Guard{Fetchers: map[string]catalogguard.Fetcher{
		"kiro": func(context.Context, repository.Connection) ([]string, error) { return []string{"live-model"}, nil },
	}}
	code, out, raw := m.call("GET", "/api/providers/"+conn.ID+"/models", "", true)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	found := false
	for _, item := range out["models"].([]any) {
		if row, _ := item.(map[string]any); row["id"] == "live-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the live list is missing: %s", raw)
	}
	// A provider with no fetcher keeps the embedded list.
	code, out, _ = m.call("GET", "/api/providers/"+conn.ID+"/models", "", true)
	if code != 200 || len(out["models"].([]any)) == 0 {
		t.Fatalf("the embedded fallback is empty")
	}
}
