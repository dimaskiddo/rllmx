package controllers

import (
	"sync"
	"testing"
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
