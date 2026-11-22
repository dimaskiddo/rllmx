package controllers

import (
	"errors"
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
	DB  func() *repository.Store
	Reg *skill.Registry
	// Dir is the data directory; the skills live in its "skills" folder.
	Dir  string
	Log  log.Log
	gate sync.Mutex
	// studio serialises the write routes, so two writes cannot interleave files
	// in one folder.
	studio sync.Mutex
}

// skillsDir is the folder the registry reads and the Studio writes.
func (c *SkillsController) skillsDir() string { return filepath.Join(c.Dir, "skills") }

// skillItem is one installed skill with the switch the settings row gives it.
type skillItem struct {
	skill.Manifest
	Enabled bool   `json:"enabled"`
	Mode    string `json:"mode"`
}

// fetchClass names why a fetch failed, without the address or the output, which
// can carry a source's credentials or a remote message.
func fetchClass(err error) string {
	switch {
	case errors.Is(err, git.ErrRefused):
		return "refused"
	case errors.Is(err, git.ErrFailed):
		return "failed"
	}
	return "error"
}

// skillList is the answer to GET /api/skills.
type skillList struct {
	Skills   []skillItem `json:"skills"`
	Problems []string    `json:"problems"`
}

// syncAnswer is the answer to POST /api/skills/sync.
type syncAnswer struct {
	Total   int      `json:"total"`
	Updated int      `json:"updated"`
	Skipped int      `json:"skipped"`
	Skills  []string `json:"skills"`
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
	get, legacy := skill.Readers(doc.GetRaw)
	items := make([]skillItem, 0, 16)
	for _, m := range c.Reg.List() {
		enabled, mode := skill.Switch(m, get, legacy)
		items = append(items, skillItem{Manifest: m, Enabled: enabled, Mode: mode})
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, skillList{Skills: items, Problems: c.Reg.Problems()})
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
	cfg := git.Config{Parent: skill.FetchDir(c.Dir), Sources: skill.Sources(doc.GetRaw("skillSources"))}
	client := network.NewClient(network.Options{ConnectTimeout: git.Timeout, BodyCap: git.MaxSize})
	dir, cleanup, err := git.NewGate().Fetch(ctx.Request().Origin().Context(), cfg, client, source, ref)
	if err != nil {
		if c.Log != nil {
			c.Log.Warningf("skills: sync could not fetch the catalog: %s", fetchClass(err))
		}
		return flat(ctx, nethttp.StatusBadGateway, "The catalog could not be fetched")
	}
	defer cleanup()
	res, err := skill.SyncECC(ctx.Request().Origin().Context(), dir, filepath.Join(c.skillsDir(), skill.CatalogDir), body.Force)
	c.Reg.Invalidate()
	if err != nil {
		if c.Log != nil {
			c.Log.Warning("skills: sync could not import the catalog")
		}
		return flat(ctx, nethttp.StatusInternalServerError, "The catalog could not be imported")
	}
	if c.Log != nil {
		c.Log.Debugf("skills: sync imported %d of %d", res.Updated, res.Total)
	}
	return ctx.Response().Json(nethttp.StatusOK, syncAnswer{Total: res.Total, Updated: res.Updated, Skipped: res.Skipped, Skills: res.Skills})
}
