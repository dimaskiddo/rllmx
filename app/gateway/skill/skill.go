package skill

import (
	"slices"
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/text"
)

// The bypass headers keep the reference's names so an existing client keeps
// working.
const (
	// BypassLocal turns the local skill router off for one request.
	BypassLocal = "x-9router-local-skill-router"
	// BypassECC turns the imported catalog router off for one request.
	BypassECC = "x-9router-skill-router"
)

// maxLocal is how many smart skills one request may receive, as the reference
// caps it.
const maxLocal = 2

// sep separates one injected prompt from the text around it, and is how a
// second run recognises its own earlier work.
const sep = "\n\n"

// Setting is the settings row's view of one skill: the two keys the dashboard
// writes, each with whether it was present at all.
type Setting struct {
	Enabled    bool
	HasEnabled bool
	Mode       string
	HasMode    bool
}

// Options is one request's skill configuration.
type Options struct {
	// Setting reads a skill's stored switch and mode. A nil reader leaves every
	// skill to its manifest.
	Setting func(id string) Setting
	// Value reads one config_schema value by its settings key, as the prompt
	// should render it. A nil reader leaves every field to its default.
	Value func(key string) (string, bool)
	// Prior names the skills an earlier turn of this conversation already
	// injected in full. A later smart match becomes a one-line reminder.
	Prior map[string]bool
	// Notice asks for the failover notice at the top of the system prompt.
	Notice bool
	ECC    ECCOptions
}

// Stats is what one request's skills did, for the redacting log line.
type Stats struct {
	// Skills are the ids injected in full.
	Skills []string
	// Reminded are the ids an earlier turn already carried.
	Reminded []string
	// Session names the skills to record for this conversation, router-prefixed.
	Session []string
	// Notice reports that the failover notice was injected.
	Notice bool
}

// Changed reports whether the request was changed.
func (s Stats) Changed() bool { return len(s.Skills)+len(s.Reminded) > 0 || s.Notice }

// Line renders the log line: skill ids only, never prompt text.
func (s Stats) Line() string {
	var parts []string
	if len(s.Skills) > 0 {
		parts = append(parts, "skills="+strings.Join(s.Skills, "+"))
	}
	if len(s.Reminded) > 0 {
		parts = append(parts, "reminded="+strings.Join(s.Reminded, "+"))
	}
	if s.Notice {
		parts = append(parts, "notice")
	}
	return strings.Join(parts, " ")
}

// Apply injects every enabled skill whose hook is a system prompt: an "always"
// skill on every request, a "smart" skill when its own topic scores. Nothing
// here returns an error; a nil registry or an unknown skill leaves the request
// as it was.
func Apply(reg *Registry, r *translate.Request, o Options) Stats {
	var st Stats
	if reg == nil || r == nil {
		return st
	}
	var always, smart []Manifest
	for _, m := range reg.List() {
		if m.Hook != "system-prompt" {
			continue
		}
		enabled, mode := Switch(m, o.Setting)
		if !enabled {
			continue
		}
		if mode == "always" {
			always = append(always, m)
			continue
		}
		smart = append(smart, m)
	}
	seen := map[string]bool{}
	inject := func(m Manifest, router string, routable bool) {
		if seen[m.ID] {
			return
		}
		if routable && o.Prior[InjectedKey(router, m.ID)] {
			if InjectPrompt(r, reminder(m.Name, routerPrefix(router))) {
				seen[m.ID] = true
				st.Reminded = append(st.Reminded, m.ID)
			}
			return
		}
		body := Render(m, reg.Prompt(m.ID), o.Value)
		if body == "" || !InjectPrompt(r, body) {
			return
		}
		seen[m.ID] = true
		st.Skills = append(st.Skills, m.ID)
		st.Session = append(st.Session, InjectedKey(router, m.ID))
	}
	if o.Notice {
		st.Notice = InjectPrompt(r, FailoverNotice)
	}
	for _, m := range always {
		inject(m, "", false)
	}
	if len(smart) > 0 || o.ECC.Enabled {
		query := LatestUser(r)
		for _, match := range ClassifyLocal(reg, smart, query) {
			if m, ok := reg.Find(match.ID); ok {
				inject(m, RouterLocal, true)
			}
		}
		for _, match := range ClassifyECC(reg, query, o.ECC) {
			if m, ok := reg.Find(match.ID); ok {
				inject(m, RouterECC, true)
			}
		}
	}
	return st
}

// The two routers' names in the per-session table and the reminder's prefix.
const (
	RouterLocal = "local"
	RouterECC   = "ecc"
)

// FailoverNotice is the reference's text, carried verbatim.
const FailoverNotice = "--- Failover Notice: Upstream model/account switched. Check prior turns for existing tool results before searching again. ---"

// InjectedKey names one skill in the per-session table, split by which router
// matched it, as the reference does.
func InjectedKey(router, id string) string {
	return router + "-" + id
}

func routerPrefix(router string) string {
	if router == RouterECC {
		return "ECC Skill"
	}
	return "Local Skill"
}

// reminder is the reference's one-line form for a skill this conversation
// already carries in full.
func reminder(name, prefix string) string {
	if name == "" {
		name = prefix
	}
	return "--- " + prefix + ": " + name +
		" (already active this conversation — see earlier turn for full instructions; do not repeat completed steps) ---"
}

// InjectPrompt appends a prompt to the request's first system message, creating
// one at the top when there is none. It reports whether the request changed; a
// prompt already present as its own segment is not added twice, so an account
// retry never doubles it.
func InjectPrompt(r *translate.Request, prompt string) bool {
	for i := range r.Messages {
		if r.Messages[i].Role != translate.RoleSystem {
			continue
		}
		msg := &r.Messages[i]
		if hasSegment(msg.Text(), prompt) {
			return false
		}
		if msg.Text() == "" {
			msg.Content = translate.TextPart(prompt)
			return true
		}
		msg.Content = append(msg.Content, translate.Part{Type: translate.PartText, Text: sep + prompt})
		return true
	}
	r.Messages = append([]translate.Message{{
		Role:    translate.RoleSystem,
		Content: translate.TextPart(prompt),
	}}, r.Messages...)
	return true
}

// hasSegment reports the prompt as its own segment of the text, so unrelated
// text that happens to contain it is left alone.
func hasSegment(body, target string) bool {
	if body == target {
		return true
	}
	for segment := range strings.SplitSeq(body, sep) {
		if segment == target {
			return true
		}
	}
	return false
}

// LatestUser is the query the routers score: the last user message, cut to the
// routing bound.
func LatestUser(r *translate.Request) string {
	for i := range slices.Backward(r.Messages) {
		if r.Messages[i].Role != translate.RoleUser {
			continue
		}
		if t := strings.TrimSpace(r.Messages[i].Text()); t != "" {
			return text.CutBytes(t, queryBytes)
		}
	}
	return ""
}
