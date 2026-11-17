package controllers

import (
	"context"
	nethttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"rllmx/app/gateway/catalog"
	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/health"
	"rllmx/app/gateway/pipeline"
	"rllmx/app/gateway/probe"
	apphttp "rllmx/app/http"
	"rllmx/app/store/repository"
)

// countingHandler answers a probe and counts how often it was asked.
type countingHandler struct {
	mu    sync.Mutex
	calls int
}

func (h *countingHandler) Handle(context.Context, pipeline.Input, pipeline.Opener) pipeline.Output {
	h.mu.Lock()
	h.calls++
	h.mu.Unlock()
	return pipeline.Output{Status: 200, Body: []byte(`{"choices":[{"message":{"content":"hi"}}]}`)}
}

func (h *countingHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.calls
}

func comboRoutes(m *mgmtRig, c *CombosController) {
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodGet, "/api/combos/suggest", c.Suggest)
	m.guard.Handle(m.router, apphttp.ClassProtected, nethttp.MethodPost, "/api/combos/suggest/accept", c.Accept)
}

func TestASuggestionWithoutProbeDataNamesTheProvidersItHas(t *testing.T) {
	m := newMgmtRig(t)
	m.ok(t, "POST", "/api/providers", `{"provider":"openai","apiKey":"k","name":"a"}`, 201)
	comboRoutes(m, &CombosController{DB: func() *repository.Store { return m.st.Store }})

	out := m.ok(t, "GET", "/api/combos/suggest", "", 200)
	models := out["models"].([]any)
	if len(models) != 1 || models[0] != "openai" {
		t.Fatalf("models %v", models)
	}
	if summary := out["summary"].(map[string]any); summary["total"] != float64(0) {
		t.Fatalf("summary %v", summary)
	}
}

func TestASuggestionRanksTheProbedModelsAndNeverSpendsOnItself(t *testing.T) {
	m := newMgmtRig(t)
	id := connID(t, m.ok(t, "POST", "/api/providers",
		`{"provider":"openai","apiKey":"k","name":"a","providerSpecificData":{"enabledModels":["gpt-4o"]}}`, 201))
	h := &countingHandler{}
	p := probe.New(probe.Options{
		Pipeline: func() (probe.Handler, error) { return h, nil },
		Nodes:    func() probe.NodeReader { return m.st.Store.Nodes() },
		KV:       func() catalog.KV { return m.st.Store.KV() },
		Registry: m.pc.Registry,
	})
	cc := &CombosController{
		DB:      func() *repository.Store { return m.st.Store },
		Probe:   func() (*probe.Prober, error) { return p, nil },
		Tracker: health.NewTracker(time.Now),
	}
	conn, _, _ := m.st.Connections().Get(context.Background(), id)
	if got := p.Connection(context.Background(), conn, true); len(got) != 1 || !got[0].OK {
		t.Fatalf("probe %+v", got)
	}
	calls := h.count()
	comboRoutes(m, cc)

	out := m.ok(t, "GET", "/api/combos/suggest", "", 200)
	models := out["models"].([]any)
	if len(models) != 1 || models[0] != "openai/gpt-4o" {
		t.Fatalf("models %v", models)
	}
	if summary := out["summary"].(map[string]any); summary["working"] != float64(1) || summary["total"] != float64(1) {
		t.Fatalf("summary %v", summary)
	}
	if h.count() != calls {
		t.Fatalf("a suggestion spent %d probes", h.count()-calls)
	}
	m.ok(t, "GET", "/api/combos/suggest?weights=1,0,0,0&limit=3&prompt=hi", "", 200)
	if h.count() != calls {
		t.Fatal("a second suggestion spent again")
	}
}

func TestABadWeightFallsBackToTheDefaults(t *testing.T) {
	m := newMgmtRig(t)
	comboRoutes(m, &CombosController{DB: func() *repository.Store { return m.st.Store }})
	for _, q := range []string{"weights=nope", "weights=1,2", "weights=1,2,3,4,5", "reliability=-1"} {
		out := m.ok(t, "GET", "/api/combos/suggest?"+q, "", 200)
		w := out["weights"].(map[string]any)
		if w["reliability"] != combo.DefaultWeights.Reliability || w["quality"] != combo.DefaultWeights.Quality {
			t.Errorf("%s: weights %v", q, w)
		}
	}
	if out := m.ok(t, "GET", "/api/combos/suggest?weights=1,0,0", "", 200); out["weights"].(map[string]any)["latency"] != float64(0) {
		t.Fatalf("weights given: %v", out["weights"])
	}
}

func TestAcceptCreatesAnAutoComboAndNeverOverwritesOne(t *testing.T) {
	m := newMgmtRig(t)
	comboRoutes(m, &CombosController{DB: func() *repository.Store { return m.st.Store }})

	out := m.ok(t, "POST", "/api/combos/suggest/accept", `{"name":"fast","models":["openai/gpt-4o"]}`, 201)
	created := out["combo"].(map[string]any)
	if created["kind"] != "auto" || created["name"] != "fast" {
		t.Fatalf("%v", created)
	}
	stored, ok, err := m.st.Combos().GetByID(context.Background(), created["id"].(string))
	if err != nil || !ok || stored.Kind != "auto" {
		t.Fatalf("%+v %v", stored, err)
	}

	code, conflict, _ := m.call("POST", "/api/combos/suggest/accept", `{"name":"fast","models":["openai/gpt-4o-mini"]}`, true)
	if code != nethttp.StatusConflict || conflict["conflict"] != true {
		t.Fatalf("%d %v", code, conflict)
	}
	again, _, _ := m.st.Combos().GetByID(context.Background(), created["id"].(string))
	if again.Models[0] != "openai/gpt-4o" {
		t.Fatalf("accept overwrote the combo: %+v", again)
	}
}

func TestAcceptRefusesANameOrListAComboCannotCarry(t *testing.T) {
	m := newMgmtRig(t)
	comboRoutes(m, &CombosController{DB: func() *repository.Store { return m.st.Store }})
	for name, body := range map[string]string{
		"no name":        `{"name":"","models":["openai/gpt-4o"]}`,
		"blank name":     `{"name":"   ","models":["openai/gpt-4o"]}`,
		"slash in name":  `{"name":"a/b","models":["openai/gpt-4o"]}`,
		"control char":   "{\"name\":\"a\\u0085b\",\"models\":[\"openai/gpt-4o\"]}",
		"no models":      `{"name":"x","models":[]}`,
		"blank model":    `{"name":"x","models":["  "]}`,
		"not json":       `nope`,
		"too many names": `{"name":"` + strings.Repeat("a", 120) + `","models":["openai/gpt-4o"]}`,
	} {
		if code, _, raw := m.call("POST", "/api/combos/suggest/accept", body, true); code != 400 {
			t.Errorf("%s: %d %s", name, code, raw)
		}
	}
}
