package controllers

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"rllmx/app/gateway/catalog"
	"rllmx/app/gateway/pipeline"
	"rllmx/app/gateway/probe"
	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

func TestABatchNeedsAModeAndAProvider(t *testing.T) {
	m := newMgmtRig(t)
	for name, body := range map[string]string{
		"no mode":             `{}`,
		"unknown mode":        `{"mode":"everything"}`,
		"provider without id": `{"mode":"provider"}`,
		"not json":            `nope`,
	} {
		if code, _, raw := m.call("POST", "/api/providers/test-batch", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}

func TestABatchTestsOnlyTheConnectionsOfItsMode(t *testing.T) {
	m := newMgmtRig(t)
	srv := newUpstream(t, 200, `{"data":[]}`)
	node := nodeFor(t, m, srv.URL, "openai-compatible", "tb")
	connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"n1"}`, node), 201))
	connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"n2"}`, node), 201))
	connID(t, m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"k1"}`, 201))

	out := m.ok(t, "POST", "/api/providers/test-batch", `{"mode":"compatible"}`, 200)
	rows := out["results"].([]any)
	if len(rows) != 2 {
		t.Fatalf("%d rows: %v", len(rows), rows)
	}
	for _, raw := range rows {
		if row := raw.(map[string]any); row["provider"] != node {
			t.Errorf("a row outside the mode: %v", row)
		}
	}
	summary := out["summary"].(map[string]any)
	if summary["total"] != float64(2) || summary["passed"] != float64(2) || summary["failed"] != float64(0) {
		t.Fatalf("summary %v", summary)
	}

	out = m.ok(t, "POST", "/api/providers/test-batch", fmt.Sprintf(`{"mode":"provider","providerId":%q}`, node), 200)
	if rows := out["results"].([]any); len(rows) != 2 {
		t.Fatalf("%d rows for the node", len(rows))
	}
}

func TestABatchLineFailureDoesNotFailTheRequest(t *testing.T) {
	m := newMgmtRig(t)
	srv := newUpstream(t, 200, `{"data":[]}`)
	node := nodeFor(t, m, srv.URL, "openai-compatible", "tf")
	id := connID(t, m.ok(t, "POST", "/api/providers", fmt.Sprintf(`{"provider":%q,"apiKey":"k","name":"a"}`, node), 201))
	// A connection whose node is gone: the row fails, the batch does not.
	ghost, err := m.st.Connections().Create(context.Background(), repository.ConnInput{
		Provider: "openai-compatible-chat-ghost", AuthType: "apikey", Name: "ghost",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := m.ok(t, "POST", "/api/providers/test-batch", `{"mode":"all"}`, 200)
	rows := out["results"].([]any)
	if len(rows) != 2 {
		t.Fatalf("%d rows: %v", len(rows), rows)
	}
	seen := map[string]map[string]any{}
	for _, raw := range rows {
		row := raw.(map[string]any)
		seen[row["connectionId"].(string)] = row
	}
	if seen[id]["valid"] != true {
		t.Fatalf("the healthy connection failed: %v", seen[id])
	}
	if g := seen[ghost.ID]; g == nil || g["valid"] != false || g["error"] != "Provider node not found" {
		t.Fatalf("the ghost row: %v", g)
	}
}

// okHandler answers every probe with a chat completion.
type okHandler struct{}

func (okHandler) Handle(context.Context, pipeline.Input, pipeline.Opener) pipeline.Output {
	return pipeline.Output{Status: 200, Body: []byte(`{"choices":[{"message":{"content":"hi"}}]}`)}
}

// emptyKV is a cache with no custom or disabled models.
type emptyKV struct{}

func (emptyKV) List(context.Context, string) (map[string]string, error) { return nil, nil }
func (emptyKV) DisabledModels(context.Context, string) ([]string, error) {
	return nil, nil
}

func fakeProbe(m *mgmtRig) func() (*probe.Prober, error) {
	return func() (*probe.Prober, error) {
		return probe.New(probe.Options{
			Pipeline: func() (probe.Handler, error) { return okHandler{}, nil },
			Nodes:    func() probe.NodeReader { return m.st.Nodes() },
			KV:       func() catalog.KV { return emptyKV{} },
			Registry: m.pc.Registry,
		}), nil
	}
}

func TestTestModelsProbesTheConnectionsOwnModels(t *testing.T) {
	m := newMgmtRig(t)
	m.pc.Probe = fakeProbe(m)
	id := connID(t, m.ok(t, "POST", "/api/providers",
		`{"provider":"openai","apiKey":"k","name":"a","providerSpecificData":{"enabledModels":["a","b"]}}`, 201))

	out := m.ok(t, "POST", "/api/providers/"+id+"/test-models", "", 200)
	rows := out["results"].([]any)
	if len(rows) != 2 {
		t.Fatalf("%d rows: %v", len(rows), rows)
	}
	first := rows[0].(map[string]any)
	if first["modelId"] != "a" || first["ok"] != true {
		t.Fatalf("%v", first)
	}
	if first["model"] != "openai/a" {
		t.Fatalf("the probed name: %v", first)
	}

	if code, _, _ := m.call("POST", "/api/providers/nope/test-models", "", true); code != 404 {
		t.Fatalf("%d for an unknown connection", code)
	}
}

func TestOneModelTestNeedsAModelAndAChatKind(t *testing.T) {
	m := newMgmtRig(t)
	mc := &ModelsController{Probe: fakeProbe(m)}
	m.guard.Handle(m.router, apphttp.ClassProtected, http.MethodPost, "/api/models/test", mc.Test)
	if code, _, _ := m.call("POST", "/api/models/test", `{}`, true); code != 400 {
		t.Fatalf("%d without a model", code)
	}
	if code, _, _ := m.call("POST", "/api/models/test", `{"model":"x","kind":"image"}`, true); code != 400 {
		t.Fatalf("%d for a non-chat kind", code)
	}
	out := m.ok(t, "POST", "/api/models/test", `{"model":"demo/m1"}`, 200)
	if out["ok"] != true || out["status"] != float64(200) {
		t.Fatalf("%v", out)
	}
}
