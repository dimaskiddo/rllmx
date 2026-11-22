package controllers

import (
	nethttp "net/http"
	"path/filepath"
	"strings"
	"sync"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/gateway/skill"
	"rllmx/app/git"
	"rllmx/app/platform/network"
	"rllmx/app/store/repository"
)

// SkillsController answers the dashboard's skills routes: what is installed,
// and the catalog sync.
type SkillsController struct {
	DB   func() *repository.Store
	Reg  *skill.Registry
	Dir  string
	Log  log.Log
	gate sync.Mutex
	// studio serialises the write routes, so two writes cannot interleave files
	// in one folder.
	studio sync.Mutex
}

// skillItem is one installed skill with the switch the settings row gives it.
type skillItem struct {
	skill.Manifest
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
}

// List answers GET /api/skills: every installed skill, its effective switch,
// and the folders that were skipped. Prompt text never leaves this route.
func (c *SkillsController) List(ctx contractshttp.Context) contractshttp.Response {
	st := storeOf(c.DB)
	if st == nil {
		return storageDown(ctx)
	}
	doc, err := st.Settings().Effective(ctx.Request().Origin().Context())
	if err != nil {
		return storageDown(ctx)
	}
	get := func(id string) skill.Setting { return skill.ReadSetting(doc.GetRaw, id) }
	items := make([]skillItem, 0, 16)
	for _, m := range c.Reg.List() {
		enabled, mode := skill.Switch(m, get)
		items = append(items, skillItem{Manifest: m, Enabled: enabled, Mode: mode})
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, map[string]any{
		"skills": items, "problems": c.Reg.Problems(),
	})
}

// syncBody is what the dashboard sends to start a catalog sync.
type syncBody struct {
	Source string `json:"source"`
	Tag    string `json:"tag"`
	Force  bool   `json:"force"`
}

// Sync answers POST /api/skills/sync. It accepts only an https repository on
// the operator's allowlist; a local path is the command line's business. One
// sync runs at a time, so two clicks cannot pile up clones.
func (c *SkillsController) Sync(ctx contractshttp.Context) contractshttp.Response {
	if !c.gate.TryLock() {
		return flat(ctx, nethttp.StatusServiceUnavailable, "A sync is already running")
	}
	defer c.gate.Unlock()
	var body syncBody
	if err := ctx.Request().Bind(&body); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	source := strings.TrimSpace(body.Source)
	if !strings.HasPrefix(source, "https://") || len(source) > 2048 {
		return flat(ctx, nethttp.StatusBadRequest, "Source must be an https repository")
	}
	st := storeOf(c.DB)
	if st == nil {
		return storageDown(ctx)
	}
	doc, err := st.Settings().Effective(ctx.Request().Origin().Context())
	if err != nil {
		return storageDown(ctx)
	}
	ref := strings.TrimSpace(body.Tag)
	if ref == "" {
		ref = "main"
	}
	cfg := git.Config{Parent: filepath.Join(c.Dir, "skills"), Sources: skill.Sources(doc.GetRaw("skillSources"))}
	client := network.NewClient(network.Options{ConnectTimeout: git.Timeout, BodyCap: git.MaxSize})
	dir, cleanup, err := git.NewGate().Fetch(ctx.Request().Origin().Context(), cfg, client, source, ref)
	if err != nil {
		if c.Log != nil {
			c.Log.Warning("skills: sync could not fetch the catalog")
		}
		return flat(ctx, nethttp.StatusBadGateway, "The catalog could not be fetched")
	}
	defer cleanup()
	res, err := skill.SyncECC(ctx.Request().Origin().Context(), dir, filepath.Join(c.Dir, "skills", "ecc-imported"), body.Force)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "The catalog could not be imported")
	}
	if c.Log != nil {
		c.Log.Debugf("skills: sync imported %d of %d", res.Updated, res.Total)
	}
	return ctx.Response().Json(nethttp.StatusOK, map[string]any{
		"total": res.Total, "updated": res.Updated, "skipped": res.Skipped, "skills": res.Skills,
	})
}
