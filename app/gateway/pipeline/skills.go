package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/conversation"
	"rllmx/app/gateway/skill"
	"rllmx/app/platform/text"
	"rllmx/app/store/document"
)

// skillsOff is true when the client asked this router to keep the request as
// it is. The two routers have their own header, as in the reference.
func (c *call) skillsOff(header string) bool {
	return strings.EqualFold(strings.TrimSpace(c.in.Header.Get(header)), "off")
}

// skillOptions reads the registry switches for this request. A nil registry, a
// missing settings reader or a failed read runs no skill.
func (c *call) skillOptions(ctx context.Context) skill.Options {
	if c.p == nil || c.p.d.Skills == nil || c.p.d.Settings == nil {
		return skill.Options{}
	}
	if c.skillsOff(skill.BypassLocal) && c.skillsOff(skill.BypassECC) {
		return skill.Options{}
	}
	doc, err := c.p.d.Settings.Effective(ctx)
	if err != nil {
		return skill.Options{}
	}
	_, prior, notice := c.sessionInfo()
	o := skill.Options{
		Setting: func(id string) skill.Setting { return readSetting(doc, id) },
		Value:   func(key string) (string, bool) { return configValue(doc, key) },
		Prior:   prior,
		Notice:  notice,
		ECC: skill.ECCOptions{
			Enabled:    rawTrue(doc, "ecc_auto_skill_routerEnabled", false) && !c.skillsOff(skill.BypassECC),
			Confidence: rawFloat(doc, "ecc_auto_skill_routerConfidence", skill.DefaultThreshold),
			MaxSkills:  rawInt(doc, "ecc_auto_skill_routerMaxSkills", 1),
		},
	}
	if c.skillsOff(skill.BypassLocal) {
		o.Setting = func(string) skill.Setting { return skill.Setting{HasEnabled: true, Enabled: false} }
	}
	return o
}

// readSetting projects the two keys the dashboard writes for one skill.
func readSetting(doc document.Doc, id string) skill.Setting {
	return skill.ReadSetting(doc.GetRaw, id)
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
	st := skill.Apply(c.p.d.Skills, c.req, c.skillOptions(ctx))
	if key, _, _ := c.sessionInfo(); key != "" && len(st.Session) > 0 {
		c.p.d.SkillSessions.Mark(key, st.Session)
	}
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

// failover records that this conversation moved to another account.
func (c *call) failover() {
	if key, _, _ := c.sessionInfo(); key != "" && c.p != nil && c.p.d.SkillSessions != nil {
		c.p.d.SkillSessions.Failover(key)
	}
}

// skillKey is the part of the response-cache key that names what the skills
// would change. The cache key hashes the client's own body, before any skill
// rewrites it, so two requests with the same words but different skills must
// not share an answer.
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
	var b strings.Builder
	b.WriteString("sig=" + c.p.d.Skills.Signature() + ";")
	for _, id := range c.rules.IDs {
		b.WriteString("rule=" + id + ";")
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
				b.WriteString(key + "=" + raw + ";")
			}
		}
	}
	fmt.Fprintf(&b, "ecc=%t/%v/%d", o.ECC.Enabled, o.ECC.Confidence, o.ECC.MaxSkills)
	sum := sha256.Sum256([]byte(b.String()))
	return "sk=" + hex.EncodeToString(sum[:6])
}

// rawFloat reads a stored number, taking the default for a missing or unusable
// value.
func rawFloat(doc document.Doc, key string, def float64) float64 {
	raw, ok := doc.GetRaw(key)
	if !ok {
		return def
	}
	if v := gjson.Parse(raw); v.Type == gjson.Number {
		return v.Float()
	}
	return def
}
