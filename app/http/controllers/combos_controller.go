package controllers

import (
	"context"
	"errors"
	"math"
	nethttp "net/http"
	"net/url"
	"strconv"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/gateway/combo"
	"rllmx/app/gateway/health"
	"rllmx/app/gateway/probe"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

const (
	// suggestDefault and suggestMax bound how many models a suggestion names.
	suggestDefault = 7
	suggestMax     = 50
	// promptLimit bounds the prompt the ranking reads.
	promptLimit = 4000
	// comboNameBytes and comboModelBytes are the stored limits a browser form
	// may not slip past.
	comboNameBytes  = 100
	comboModelBytes = 256
	comboModelsMax  = 50
)

// CombosController manages the combos and suggests one. It never spends on a
// suggestion: the ranking reads cached probe results, the health table and the
// price table.
type CombosController struct {
	DB      func() *repository.Store
	Probe   func() (*probe.Prober, error)
	Tracker *health.Tracker
	Prices  func() (*upstream.Table, error)
	// Registry feeds the presets; nil leaves the preset routes out.
	Registry *upstream.Registry
	// Changed drops the routing view after a write.
	Changed func()
	// Reset forgets a combo's rotation and sticky choice after a write.
	Reset func(names ...string)
	Log   log.Log
}

// comboJSON is one combo as the dashboard reads it.
func comboJSON(c repository.Combo) contractshttp.Json {
	models := c.Models
	if models == nil {
		models = []string{}
	}
	return contractshttp.Json{
		"id": c.ID, "name": c.Name, "kind": c.Kind, "models": models,
		"createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt,
	}
}

// changed runs the hooks a combo write needs: the routing view, the rotation
// and the sticky choice.
func (c *CombosController) changed(names ...string) {
	if c.Reset != nil {
		c.Reset(names...)
	}
	if c.Changed != nil {
		c.Changed()
	}
}

// Suggest answers GET /api/combos/suggest.
func (c *CombosController) Suggest(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	rctx := ctx.Request().Origin().Context()
	q := ctx.Request().Origin().URL.Query()
	weights := suggestWeights(q)
	profile := combo.Analyse(text.CutRunes(q.Get("prompt"), promptLimit))
	limit := suggestLimit(q)

	active := true
	conns, err := st.Connections().List(rctx, repository.ConnFilter{IsActive: &active})
	if err != nil {
		return storageDown(ctx)
	}
	states := map[string]health.State{}
	if c.Tracker != nil {
		states = c.Tracker.Snapshot()
	}
	p := c.prober()
	candidates := make([]combo.Candidate, 0, len(conns))
	tested := make([]probe.Result, 0, len(conns))
	for _, conn := range conns {
		if p == nil {
			break
		}
		for _, r := range p.Probed(rctx, conn) {
			tested = append(tested, r)
			if !r.OK {
				continue
			}
			candidates = append(candidates, c.candidate(conn, r, states))
		}
	}
	models := combo.Rank(candidates, weights, profile, limit)
	if len(models) == 0 {
		// Nothing has been probed yet: name the providers that have an account,
		// skipping a breaker that is open, so a first suggestion is still useful.
		models = c.unprobed(rctx, conns, p, limit)
	}
	working, failed := 0, 0
	for _, r := range tested {
		if r.OK {
			working++
		} else {
			failed++
		}
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
		"models": models,
		"tested": tested,
		"summary": contractshttp.Json{
			"total": len(tested), "working": working, "failed": failed,
		},
		"weights": contractshttp.Json{
			"reliability": weights.Reliability, "latency": weights.Latency,
			"cost": weights.Cost, "quality": weights.Quality,
		},
		"context": contractshttp.Json{
			"reasoning": profile.Reasoning, "code": profile.Code, "mini": profile.Mini,
		},
	})
}

func (c *CombosController) store() *repository.Store {
	if c.DB == nil {
		return nil
	}
	return c.DB()
}

func (c *CombosController) prober() *probe.Prober {
	if c.Probe == nil {
		return nil
	}
	p, err := c.Probe()
	if err != nil {
		return nil
	}
	return p
}

// candidate is one probed model as the ranking reads it.
func (c *CombosController) candidate(conn repository.Connection, r probe.Result, states map[string]health.State) combo.Candidate {
	state := states[conn.Provider]
	latency := float64(r.LatencyMs)
	if latency <= 0 {
		latency = float64(state.EMALatency)
	}
	if latency <= 0 {
		latency = 1000
	}
	reliability := 1.0
	if state.SuccessRate != nil {
		reliability = *state.SuccessRate
	}
	out := combo.Candidate{Model: r.Model, Reliability: reliability, LatencyMs: latency}
	if c.Prices != nil {
		if table, err := c.Prices(); err == nil && table != nil {
			in, kin := table.Cost(conn.Provider, r.ModelID, upstream.Usage{Prompt: 1_000_000})
			spend, kout := table.Cost(conn.Provider, r.ModelID, upstream.Usage{Completion: 1_000_000})
			if kin && kout {
				out.CostPerM, out.KnownCost = (in+spend)/2, true
			}
		}
	}
	return out
}

// unprobed names one model per provider with an account, in connection order.
func (c *CombosController) unprobed(ctx context.Context, conns []repository.Connection, p *probe.Prober, limit int) []string {
	out := make([]string, 0, limit)
	seen := map[string]bool{}
	for _, conn := range conns {
		if len(out) >= limit {
			break
		}
		if c.Tracker != nil && c.Tracker.Open(conn.Provider) {
			continue
		}
		name := conn.Provider
		if p != nil {
			if list, err := p.Models(ctx, conn); err == nil && len(list) > 0 {
				name = list[0]
			}
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// suggestWeights reads the weights, falling back to the defaults: this is a
// hint, and a typo must not fail the suggestion.
func suggestWeights(q url.Values) combo.Weights {
	w := combo.DefaultWeights
	if raw := strings.TrimSpace(q.Get("weights")); raw != "" {
		parts := strings.Split(raw, ",")
		if len(parts) < 3 || len(parts) > 4 {
			return combo.DefaultWeights
		}
		vals := make([]float64, 4)
		for i, part := range parts {
			v, ok := weightOf(part)
			if !ok {
				return combo.DefaultWeights
			}
			vals[i] = v
		}
		if len(parts) == 3 {
			vals[3] = combo.DefaultWeights.Quality
		}
		return combo.Weights{Reliability: vals[0], Latency: vals[1], Cost: vals[2], Quality: vals[3]}
	}
	set := func(name string, dst *float64) {
		if v, ok := weightOf(q.Get(name)); ok {
			*dst = v
		}
	}
	set("reliability", &w.Reliability)
	set("latency", &w.Latency)
	set("cost", &w.Cost)
	set("quality", &w.Quality)
	return w
}

func weightOf(raw string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	return v, true
}

func suggestLimit(q url.Values) int {
	raw := q.Get("limit")
	if raw == "" {
		raw = q.Get("count")
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return suggestDefault
	}
	return min(n, suggestMax)
}

type acceptBody struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

// Accept answers POST /api/combos/suggest/accept: it creates the suggested
// combo as an auto combo and never overwrites one, whatever its kind.
func (c *CombosController) Accept(ctx contractshttp.Context) contractshttp.Response {
	st := c.store()
	if st == nil {
		return storageDown(ctx)
	}
	var b acceptBody
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	name, models, msg := validCombo(b, 1)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	created, err := st.Combos().Create(ctx.Request().Origin().Context(), name, "auto", models)
	if errors.Is(err, repository.ErrComboNameTaken) {
		return ctx.Response().Json(nethttp.StatusConflict, contractshttp.Json{
			"error": "A combo with that name exists", "conflict": true,
		})
	}
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to create the combo")
	}
	c.changed(name)
	return ctx.Response().Json(nethttp.StatusCreated, contractshttp.Json{"combo": comboJSON(created)})
}

// validCombo checks a name and a member list, and reports the first thing wrong
// in the words a form shows. min is how few members are allowed: a suggested
// combo needs one, a plain create may be filled later.
func validCombo(b acceptBody, min int) (string, []string, string) {
	name, msg := comboName(b.Name)
	if msg != "" {
		return "", nil, msg
	}
	models, msg := comboModels(b.Models, min)
	if msg != "" {
		return "", nil, msg
	}
	return name, models, ""
}

// comboName checks a combo name.
func comboName(raw string) (string, string) {
	name := strings.TrimSpace(raw)
	switch {
	case name == "":
		return "", "A combo needs a name"
	case len(name) > comboNameBytes || text.HasControl(name):
		return "", "Invalid combo name"
	case strings.Contains(name, "/"):
		return "", "A combo name cannot contain a slash"
	}
	return name, ""
}

// comboModels checks a member list of at least min and at most the stored
// maximum.
func comboModels(in []string, min int) ([]string, string) {
	if len(in) < min || len(in) > comboModelsMax {
		if min == 0 {
			return nil, "A combo can have at most 50 models"
		}
		return nil, "A combo needs 1 to 50 models"
	}
	models := make([]string, 0, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" || len(m) > comboModelBytes || text.HasControl(m) {
			return nil, "Invalid model in the combo"
		}
		models = append(models, m)
	}
	return models, ""
}

// comboKind checks the free-form kind a media combo carries.
func comboKind(raw string) (string, string) {
	kind := strings.TrimSpace(raw)
	if len(kind) > 32 || text.HasControl(kind) {
		return "", "Invalid combo kind"
	}
	return kind, ""
}
