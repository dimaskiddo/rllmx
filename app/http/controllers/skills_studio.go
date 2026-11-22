package controllers

import (
	"encoding/json"
	"errors"
	"io"
	nethttp "net/http"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/gateway/skill"
	"rllmx/app/platform/text"
)

// studioItem is one skill as the Studio reads it: the manifest, the switch, the
// prompt and the rules, and whether this router may rewrite the folder.
type studioItem struct {
	skill.Manifest
	Enabled  bool            `json:"enabled"`
	Mode     string          `json:"mode"`
	Editable bool            `json:"editable"`
	Prompt   string          `json:"promptTemplate,omitempty"`
	Rules    json.RawMessage `json:"rules,omitempty"`
}

// Get answers GET /api/skills/{id}: one installed skill, its prompt and rules.
func (c *SkillsController) Get(ctx contractshttp.Context) contractshttp.Response {
	id := ctx.Request().Route("id")
	m, ok := c.Reg.Find(id)
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "No such skill")
	}
	item, code, msg := c.studioItem(ctx, m)
	if code != nethttp.StatusOK {
		return flat(ctx, code, msg)
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, item)
}

// Create answers POST /api/skills: a new custom skill under the skills folder.
func (c *SkillsController) Create(ctx contractshttp.Context) contractshttp.Response {
	return c.write(ctx, nethttp.StatusCreated, func(d skill.Draft) error {
		return skill.Create(c.skillsDir(), d)
	}, "")
}

// Update answers PUT /api/skills/{id}. Only a custom folder is rewritten; a
// built-in, a catalog import or a hand-placed folder is refused.
func (c *SkillsController) Update(ctx contractshttp.Context) contractshttp.Response {
	id := ctx.Request().Route("id")
	return c.write(ctx, nethttp.StatusOK, func(d skill.Draft) error {
		return skill.Update(c.skillsDir(), id, d)
	}, id)
}

// Delete answers DELETE /api/skills/{id}.
func (c *SkillsController) Delete(ctx contractshttp.Context) contractshttp.Response {
	c.studio.Lock()
	defer c.studio.Unlock()
	id := ctx.Request().Route("id")
	err := skill.Delete(c.skillsDir(), id)
	c.Reg.Invalidate()
	if err != nil {
		return studioError(ctx, err)
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(nethttp.StatusOK, struct {
		Success bool `json:"success"`
	}{true})
}

// write runs one Studio change under the write lock and answers the stored
// skill.
func (c *SkillsController) write(ctx contractshttp.Context, status int, apply func(skill.Draft) error, id string) contractshttp.Response {
	d, msg := readDraft(ctx)
	if msg != "" {
		return flat(ctx, nethttp.StatusBadRequest, msg)
	}
	if id == "" {
		id = strings.TrimSpace(d.ID)
	}
	c.studio.Lock()
	err := apply(d)
	c.studio.Unlock()
	c.Reg.Invalidate()
	if err != nil {
		return studioError(ctx, err)
	}
	m, ok := c.Reg.Find(id)
	if !ok {
		return flat(ctx, nethttp.StatusInternalServerError, "The skill was written but could not be read back")
	}
	item, code, msg := c.studioItem(ctx, m)
	if code != nethttp.StatusOK {
		return flat(ctx, code, msg)
	}
	ctx.Response().Header("Cache-Control", "no-store")
	return ctx.Response().Json(status, item)
}

// studioItem reads one skill's prompt, rules and effective switch.
func (c *SkillsController) studioItem(ctx contractshttp.Context, m skill.Manifest) (studioItem, int, string) {
	st := storeOf(c.DB)
	if st == nil {
		return studioItem{}, nethttp.StatusServiceUnavailable, "Storage is not available"
	}
	doc, err := st.Settings().Effective(ctx.Request().Origin().Context())
	if err != nil {
		return studioItem{}, nethttp.StatusServiceUnavailable, "Storage is not available"
	}
	get, legacy := skill.Readers(doc.GetRaw)
	enabled, mode := skill.Switch(m, get, legacy)
	rules, err := skill.RulesFile(c.skillsDir(), m.ID)
	if err != nil {
		return studioItem{}, nethttp.StatusInternalServerError, "The skill's rules could not be read"
	}
	return studioItem{
		Manifest: m, Enabled: enabled, Mode: mode, Editable: skill.Editable(m),
		Prompt: c.Reg.Prompt(m.ID), Rules: rules,
	}, nethttp.StatusOK, ""
}

// readDraft decodes one Studio body under the shared body cap and refuses the
// fields the router never accepts: anything that would name a process, or a
// value that claims the folder is the catalog's.
func readDraft(ctx contractshttp.Context) (skill.Draft, string) {
	r := ctx.Request().Origin()
	r.Body = nethttp.MaxBytesReader(ctx.Response().Writer(), r.Body, maxBodyBytes)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return skill.Draft{}, "Invalid request body"
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(raw, &shape); err != nil {
		return skill.Draft{}, "Invalid request body"
	}
	for _, key := range skill.RefusedDraft {
		if _, ok := shape[key]; ok {
			return skill.Draft{}, "The field " + key + " is not accepted"
		}
	}
	var d skill.Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return skill.Draft{}, "Invalid request body"
	}
	return d, ""
}

// studioError maps a Studio refusal onto the status and message the dashboard
// reads. The message is the package's own text, cut and stripped, so a crafted
// manifest cannot inject control characters into the answer.
func studioError(ctx contractshttp.Context, err error) contractshttp.Response {
	msg := text.CutRunes(text.StripControl(err.Error()), 200)
	switch {
	case errors.Is(err, skill.ErrInvalid):
		return flat(ctx, nethttp.StatusBadRequest, msg)
	case errors.Is(err, skill.ErrExists):
		return flat(ctx, nethttp.StatusConflict, "A skill with that id exists")
	case errors.Is(err, skill.ErrNotFound):
		return flat(ctx, nethttp.StatusNotFound, "No such skill")
	case errors.Is(err, skill.ErrNotCustom):
		return flat(ctx, nethttp.StatusForbidden, "Only a custom skill can be changed here")
	}
	return flat(ctx, nethttp.StatusInternalServerError, "The skill could not be saved")
}
