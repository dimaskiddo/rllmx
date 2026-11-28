package skill

import (
	"strings"

	"rllmx/app/llm/translate"
	"rllmx/app/platform/relevance"
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
	// Legacy reads a switch a manifest names by its whole key. Nil ignores it.
	Legacy func(key string) (enabled, ok bool)
	// Value reads one config_schema value by its settings key, as the prompt
	// should render it. A nil reader leaves every field to its default.
	Value func(key string) (string, bool)
	// Replace answers a skill's prompt from somewhere other than its folder, as
	// the memory a local agent keeps. An empty text with ok means this turn has
	// nothing to inject.
	Replace func(id string) (text string, ok bool)
	// Prior names the skills an earlier turn of this conversation already
	// injected in full. A later smart match becomes a one-line reminder.
	Prior map[string]bool
	// Notice asks for the failover notice at the top of the system prompt.
	Notice bool
	// Off reports a group the operator switched off, by its source: the
	// built-in set, "custom" or "ecc". Nil leaves every group on.
	Off func(source string) bool
	ECC ECCOptions
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
	// Via names how each injected skill was chosen: "always", or the router that
	// matched it.
	Via map[string]string
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

// Names is the compact list the usage row keeps: the skills this turn carried,
// then the ones an earlier turn already carried.
func (s Stats) Names() string {
	seen := map[string]bool{}
	out := make([]string, 0, len(s.Skills)+len(s.Reminded))
	for _, id := range append(append([]string{}, s.Skills...), s.Reminded...) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return strings.Join(out, ",")
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
	always, smart := partitionSkills(reg, o)
	ap := &applier{reg: reg, r: r, o: o, seen: map[string]bool{}}
	if o.Notice {
		ap.st.Notice = r.AppendSystem(failoverNotice)
	}
	for _, m := range always {
		ap.inject(m, "", false)
	}
	if len(smart) > 0 || o.ECC.Enabled {
		query := LatestUser(r)
		for _, match := range ClassifyLocal(reg, smart, query) {
			if m, ok := reg.Find(match.ID); ok {
				ap.inject(m, routerLocal, true)
			}
		}
		for _, match := range ClassifyECC(reg, query, o.ECC) {
			if m, ok := reg.Find(match.ID); ok {
				ap.inject(m, routerECC, true)
			}
		}
	}
	return ap.st
}

// partitionSkills splits the enabled system-prompt skills into those that go on
// every request and those a topic score decides.
func partitionSkills(reg *Registry, o Options) (always, smart []Manifest) {
	for _, m := range reg.List() {
		if m.Hook != "system-prompt" {
			continue
		}
		if o.Off != nil && o.Off(m.Source) {
			continue
		}
		enabled, mode := Switch(m, o.Setting, o.Legacy)
		if !enabled {
			continue
		}
		if mode == "always" {
			always = append(always, m)
			continue
		}
		smart = append(smart, m)
	}
	return always, smart
}

// applier carries one Apply call's state: what has been injected and the stats.
type applier struct {
	reg  *Registry
	r    *translate.Request
	o    Options
	seen map[string]bool
	st   Stats
}

func (a *applier) inject(m Manifest, router string, routable bool) {
	if a.seen[m.ID] {
		return
	}
	if routable && a.o.Prior[injectedKey(router, m.ID)] {
		if a.r.AppendSystem(reminder(m.Name, routerPrefix(router))) {
			a.seen[m.ID] = true
			a.st.Reminded = append(a.st.Reminded, m.ID)
		}
		return
	}
	prompt := a.reg.Prompt(m.ID)
	// REA replaces the client's own security-policy paragraph before its
	// guidance goes in, so the two never travel together.
	if m.ID == reaSkillID {
		stripSafetyParagraphs(a.r)
	}
	if a.o.Replace != nil {
		if text, ok := a.o.Replace(m.ID); ok {
			if text == "" {
				return
			}
			prompt = text
		}
	}
	body := Render(m, prompt, a.o.Value)
	if body == "" || !a.r.AppendSystem(body) {
		return
	}
	a.seen[m.ID] = true
	a.st.Skills = append(a.st.Skills, m.ID)
	a.st.Session = append(a.st.Session, injectedKey(router, m.ID))
	if a.st.Via == nil {
		a.st.Via = map[string]string{}
	}
	via := router
	if via == "" {
		via = "always"
	}
	a.st.Via[m.ID] = via
}

// The two routers' names in the per-session table and the reminder's prefix.
const (
	routerLocal = "local"
	routerECC   = "ecc"
)

// failoverNotice is the reference's text, carried verbatim.
const failoverNotice = "--- Failover Notice: Upstream model/account switched. Check prior turns for existing tool results before searching again. ---"

// injectedKey names one skill in the per-session table, split by which router
// matched it, as the reference does.
func injectedKey(router, id string) string {
	return router + "-" + id
}

func routerPrefix(router string) string {
	if router == routerECC {
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

// LatestUser is the query the routers score: the last user message, cut to the
// routing bound.
func LatestUser(r *translate.Request) string {
	return relevance.Cut(r.LastUserText())
}
