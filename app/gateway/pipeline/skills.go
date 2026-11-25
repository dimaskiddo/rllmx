package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/conversation"
	"rllmx/app/gateway/hermes"
	"rllmx/app/gateway/plan"
	"rllmx/app/gateway/skill"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

// headerOff is true when the client asked, with this header, for the request to be
// kept as it is. Each router and saver has its own header, as in the reference.
func (c *call) headerOff(header string) bool {
	return strings.EqualFold(strings.TrimSpace(c.in.Header.Get(header)), "off")
}

// skillsBypassed is true when the client switched both skill routers off, which
// leaves the request with no skill, no rule and no notice.
func (c *call) skillsBypassed() bool {
	return c.headerOff(skill.BypassLocal) && c.headerOff(skill.BypassECC)
}

// internalHeader is the whole header set a call this router makes for itself
// carries: the client's User-Agent, which decides how a body is shaped, and every
// bypass switch. No session id or other client header reaches it, so it runs no
// skill, rule, saver or plan and marks nothing in the conversation.
func (c *call) internalHeader() http.Header {
	h := http.Header{}
	if ua := c.in.Header.Get("User-Agent"); ua != "" {
		h.Set("User-Agent", ua)
	}
	for _, name := range []string{skill.BypassLocal, skill.BypassECC, plan.BypassHeader, saverBypassHeader} {
		h.Set(name, "off")
	}
	return h
}

// skillsOff reports the operator's global switch. Every place a skill could act
// reads it: empty options would still leave a manifest's default in force.
func (c *call) skillsOff(ctx context.Context) bool {
	if c.p == nil || c.p.d.Settings == nil {
		return false
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return false
	}
	return !doc.Bool("globalSkillsEnabled", true)
}

// skillOptions reads the registry switches for this request. A nil registry, a
// missing settings reader or a failed read runs no skill.
func (c *call) skillOptions(ctx context.Context) skill.Options {
	if c.p == nil || c.p.d.Skills == nil || c.p.d.Settings == nil {
		return skill.Options{}
	}
	if c.skillsBypassed() {
		return skill.Options{}
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return skill.Options{}
	}
	if !doc.Bool("globalSkillsEnabled", true) {
		return skill.Options{}
	}
	_, prior, notice := c.sessionInfo()
	setting, legacy := skill.Readers(doc.GetRaw)
	o := skill.Options{
		Off:     groupOff(doc),
		Replace: c.hermesBlock(doc),
		Setting: setting,
		Legacy:  legacy,
		Value:   func(key string) (string, bool) { return configValue(doc, key) },
		Prior:   prior,
		Notice:  notice,
		ECC: skill.ECCOptions{
			Enabled:    doc.Bool("ecc_auto_skill_routerEnabled", false) && !docBoolOff(doc, "eccSkillsEnabled") && !c.headerOff(skill.BypassECC),
			Confidence: doc.Float("ecc_auto_skill_routerConfidence", skill.DefaultThreshold),
			MaxSkills:  doc.Int("ecc_auto_skill_routerMaxSkills", 1),
		},
	}
	if c.headerOff(skill.BypassLocal) {
		o.Setting = func(string) skill.Setting { return skill.Setting{HasEnabled: true, Enabled: false} }
	}
	return o
}

// groupOff reports the skill groups the operator switched off. A group is on
// unless its key says otherwise, and a group switch never rewrites a row's own
// switch.
func groupOff(doc document.Doc) func(source string) bool {
	return func(source string) bool {
		switch source {
		case "custom":
			return docBoolOff(doc, "customSkillsEnabled")
		case "ecc":
			return docBoolOff(doc, "eccSkillsEnabled")
		}
		return docBoolOff(doc, "builtinSkillsEnabled")
	}
}

// docBoolOff reports a setting that is off only when it is explicitly false.
func docBoolOff(doc document.Doc, key string) bool { return !doc.Bool(key, true) }

// hermesBlock is the memory the agent skill injects in place of its prompt. It
// reads once per request and answers nothing when the agent has no notes, so a
// skill switched on with an empty store says nothing.
func (c *call) hermesBlock(doc document.Doc) func(string) (string, bool) {
	if c.p == nil || c.p.d.Hermes == nil || c.headerOff(hermes.Bypass) {
		return nil
	}
	limit := int(doc.Int("hermes-toolkit_char_limit", 0))
	notes := c.p.d.Hermes.Block(limit)
	protocol := c.p.d.Skills.Prompt("hermes-toolkit")
	return func(id string) (string, bool) {
		if id != "hermes-toolkit" {
			return "", false
		}
		if notes == "" {
			return "", true
		}
		if protocol == "" {
			return notes, true
		}
		return protocol + "\n\n" + notes, true
	}
}

// configValue reads one config_schema value as the prompt renders it: a string
// unquoted, a switch as true or false, a number as it was stored.
func configValue(doc document.Doc, key string) (string, bool) {
	raw, ok := doc.GetRaw(key)
	if !ok {
		return "", false
	}
	v := gjson.Parse(raw)
	switch v.Type {
	case gjson.String:
		return v.String(), true
	case gjson.True, gjson.False:
		return v.Raw, true
	case gjson.Number:
		return v.Raw, true
	}
	return "", false
}

// applySkills runs the skill routers on the hub request before it is written,
// and logs one line of ids when anything was injected.
func (c *call) applySkills(ctx context.Context) {
	if c.p == nil || c.p.d.Skills == nil {
		return
	}
	// Both bypass headers ask for an untouched request. A nil settings reader
	// would still leave every "always" skill to its manifest, so the routers are
	// skipped outright.
	if c.skillsBypassed() {
		return
	}
	// Empty options do not switch a skill off, so the global switch is checked
	// here as well as in skillOptions.
	if c.skillsOff(ctx) {
		return
	}
	st := skill.Apply(c.p.d.Skills, c.req, c.skillOptions(ctx))
	c.skillStats = st
	// Kept until the turn is answered: a turn that failed carried a prompt no
	// model read, so a retry must carry it again in full.
	c.sessionPending = append(c.sessionPending, st.Session...)
	if st.Changed() && c.p.d.Log != nil {
		c.p.d.Log.Debugf("skills: %s", st.Line())
	}
}

// maxSessionID is the longest conversation id the skill cache accepts.
const maxSessionID = 256

// sessionInfo resolves this request's conversation once: the cache key, the
// skills an earlier turn carried, and whether that conversation failed over. A
// request with no session header keeps no state.
func (c *call) sessionInfo() (key string, prior map[string]bool, notice bool) {
	if c.sessionLoaded {
		return c.sessionKey, c.sessionPrior, c.sessionNotice
	}
	c.sessionLoaded = true
	var sid string
	for _, name := range []string{"X-Session-Id", "Session-Id"} {
		if v := strings.TrimSpace(c.in.Header.Get(name)); v != "" {
			sid = v
			break
		}
	}
	if text.HasControl(sid) || len(sid) > maxSessionID {
		return "", nil, false
	}
	c.sessionKey = conversation.SessionKey(c.in.Caller.ID, sid)
	if c.sessionKey == "" || c.p == nil || c.p.d.SkillSessions == nil {
		return c.sessionKey, nil, false
	}
	c.sessionPrior = c.p.d.SkillSessions.Injected(c.sessionKey)
	c.sessionNotice = c.p.d.SkillSessions.HadFailover(c.sessionKey)
	return c.sessionKey, c.sessionPrior, c.sessionNotice
}

// markSkills records the skills an answered turn carried in full.
func (c *call) markSkills(status int) {
	if status < 200 || status > 299 || len(c.sessionPending) == 0 {
		return
	}
	if key, _, _ := c.sessionInfo(); key != "" && c.p != nil && c.p.d.SkillSessions != nil {
		c.p.d.SkillSessions.Mark(key, c.sessionPending)
	}
}

// failover marks the conversation so its later turns carry the continuity notice.
func (c *call) failover() {
	if key, _, _ := c.sessionInfo(); key != "" && c.p != nil && c.p.d.SkillSessions != nil {
		c.p.d.SkillSessions.Failover(key)
	}
}

// skillKey is the part of the response-cache key that names what the skills
// would change. The cache key hashes the client's own body, before any skill
// rewrites it, so two requests with the same words but different skills must
// not share an answer. It is built from the options the request would run with,
// so what a bypass, a session or a notice changes is in it too.
func (c *call) skillKey(ctx context.Context) string {
	if c.p == nil || c.p.d.Skills == nil || c.p.d.Settings == nil {
		return ""
	}
	o := c.skillOptions(ctx)
	if o.Setting == nil && !o.ECC.Enabled {
		return ""
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "sig=%s;local=%t;notice=%t;", c.p.d.Skills.Signature(), !c.headerOff(skill.BypassLocal), o.Notice)
	for _, id := range slices.Sorted(maps.Keys(o.Prior)) {
		fmt.Fprintf(h, "prior=%s;", id)
	}
	for _, id := range c.rules.IDs {
		fmt.Fprintf(h, "rule=%s;", id)
	}
	for _, m := range c.p.d.Skills.List() {
		keys := []string{m.ID + "Enabled", m.ID + "RoutingMode"}
		if m.LegacyEnabledKey != "" {
			keys = append(keys, m.LegacyEnabledKey)
		}
		for _, f := range m.Config {
			keys = append(keys, skill.SettingKey(m, f))
		}
		for _, key := range keys {
			if raw, ok := doc.GetRaw(key); ok {
				fmt.Fprintf(h, "%s=%s;", key, raw)
			}
		}
	}
	fmt.Fprintf(h, "ecc=%t/%v/%d", o.ECC.Enabled, o.ECC.Confidence, o.ECC.MaxSkills)
	return "sk=" + hex.EncodeToString(h.Sum(nil)[:6])
}
