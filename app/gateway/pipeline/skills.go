package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/gateway/skill"
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
	o := skill.Options{
		Setting: func(id string) skill.Setting { return readSetting(doc, id) },
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

// applySkills runs the skill routers on the hub request before it is written,
// and logs one line of ids when anything was injected.
func (c *call) applySkills(ctx context.Context) {
	if c.p == nil || c.p.d.Skills == nil {
		return
	}
	st := skill.Apply(c.p.d.Skills, c.req, c.skillOptions(ctx))
	if st.Changed() && c.p.d.Log != nil {
		c.p.d.Log.Debugf("skills: %s", st.Line())
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
	for _, m := range c.p.d.Skills.List() {
		keys := []string{m.ID + "Enabled", m.ID + "RoutingMode"}
		if m.LegacyEnabledKey != "" {
			keys = append(keys, m.LegacyEnabledKey)
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
