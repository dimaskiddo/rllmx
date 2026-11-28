package pipeline

import (
	"strconv"
	"strings"

	"rllmx/app/gateway/saver"
	"rllmx/app/gateway/skill"
	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/platform/failure"
	"rllmx/app/platform/text"
	"rllmx/app/store/analytics"
)

// setUsage puts the upstream's own counts and their price in the row. The
// client buffer is never added here.
func (c *call) setUsage(u translate.Usage) {
	r := &c.row
	r.PromptTokens, r.CompletionTokens = u.Prompt, u.Completion
	r.CachedTokens, r.CacheWriteTokens, r.ReasoningTokens = u.Cached, u.CacheWrite, u.Reasoning
	r.Estimated = u.Estimated
	if c.p.d.Prices == nil {
		return
	}
	cost, known := c.p.d.Prices.Cost(c.dest.provider, c.dest.model, upstream.Usage{
		Prompt: u.Prompt, Completion: u.Completion, Cached: u.Cached, CacheCreation: u.CacheWrite, Reasoning: u.Reasoning,
	})
	r.Cost, r.CostKnown = cost, known
}

// finish enqueues the one row the request leaves.
func (c *call) finish(out Output) {
	c.markSkills(out.Status)
	r := &c.row
	r.Status = out.Status
	if c.rowStatus != 0 {
		r.Status = c.rowStatus
	}
	r.ErrorClass = c.errClass
	if r.ErrorClass == "" {
		r.ErrorClass = failure.ClassOf(out.Status)
	}
	r.LatencyMs = c.p.d.Now().Sub(c.start).Milliseconds()
	r.Savers, r.Skills = featureNames(c.saverStats, c.skillStats)
	if c.p.d.Log != nil {
		c.p.d.Log.Infof("%s", doneLine(*r))
	}
	c.p.d.Usage.Enqueue(*r)
}

// logRoute writes the lines that say where an attempt goes and what acted on
// it. Ids, levels and counts only: no prompt text, no address, no key.
func (c *call) logRoute() {
	if c.p.d.Log == nil {
		return
	}
	c.p.d.Log.Infof("%s", c.routeLine())
	if line := c.featureLine(); line != "" {
		c.p.d.Log.Infof("%s", line)
	}
}

// logName makes a client-chosen name safe to put in a log line.
func logName(s string) string {
	return text.CutBytes(text.StripControl(s), 96)
}

// routeLine says what the client asked for, where it went and in what shape.
func (c *call) routeLine() string {
	var b strings.Builder
	b.WriteString("POST " + logName(c.asked) + " → " + logName(c.dest.provider) + "/" + logName(c.dest.model))
	b.WriteString(" · FMT " + logName(c.format) + "→" + logName(c.dest.format))
	if c.clientStream {
		b.WriteString(" · STREAM")
	} else {
		b.WriteString(" · JSON")
	}
	if c.req != nil {
		b.WriteString(" · " + strconv.Itoa(len(c.req.Messages)) + " MSG")
		if n := len(c.req.Tools); n > 0 {
			b.WriteString(" · " + strconv.Itoa(n) + " TOOL")
		}
		if t := c.req.Thinking; t.Set() {
			b.WriteString(" · THINK " + logName(text.FirstNonEmpty(t.Level, t.Mode)))
		}
		// The one case an image does not reach the model: a table that says the
		// model cannot read it. The line names no content, only the decision.
		if translate.DetectNeeds(c.req).Vision && !c.p.d.Caps.For(c.dest.provider, c.dest.model).ReadsImages() {
			b.WriteString(" · IMAGE omitted")
		}
	}
	if name := c.row.ComboName; name != "" {
		b.WriteString(` · COMBO "` + logName(name) + `"`)
	}
	b.WriteString(" · ACC " + logName(c.row.ConnectionID))
	b.WriteString(" · try " + strconv.Itoa(c.attempt))
	return b.String()
}

// featureNames splits the two feature strings for the usage row. The watermark
// remover rides the skill registry but is a fixed transform, so its prompt
// changes the request like a saver and it is filed with them.
func featureNames(st saver.Stats, ss skill.Stats) (savers, skills string) {
	savers = st.Names()
	var kept []string
	for _, id := range strings.Split(ss.Names(), ",") {
		switch {
		case id == "":
		case id == watermarkRemover:
			savers = joinFeature(savers, id)
		default:
			kept = append(kept, id)
		}
	}
	return savers, strings.Join(kept, ",")
}

// watermarkRemover is the one skill the usage row files with the savers.
const watermarkRemover = "watermarks-remover"

// joinFeature appends one name to a comma-joined list unless it is already there.
func joinFeature(list, name string) string {
	if list == "" {
		return name
	}
	for _, have := range strings.Split(list, ",") {
		if have == name {
			return list
		}
	}
	return list + "," + name
}

// featureLine is the request's saver and skill summary, in one fixed order:
// each saver's state, then the skills that acted, once. A saver that was on
// with nothing to do says SKIPPED; one that was off says OFF.
func (c *call) featureLine() string {
	st, ss := c.saverStats, c.skillStats
	injected := map[string]bool{}
	for _, id := range append(append([]string{}, ss.Skills...), ss.Reminded...) {
		injected[id] = true
	}
	skipped := map[string]bool{}
	for _, name := range st.Skipped {
		skipped[strings.ToLower(name)] = true
	}
	level := func(name string) bool {
		for _, l := range st.Levels {
			if n, _, _ := strings.Cut(l, "="); strings.EqualFold(n, name) {
				return true
			}
		}
		return false
	}
	parts := []string{
		rtkState(st, skipped["rtk"]),
		"CAVEMAN " + saverState(level("caveman"), skipped["caveman"]),
		"PONYTAIL " + saverState(level("ponytail"), skipped["ponytail"]),
	}
	if st.CustomChars > 0 {
		parts = append(parts, "CUSTOMPROMPT ON ("+strconv.Itoa(st.CustomChars)+"c)")
	}
	parts = append(parts,
		"ALLOW REA "+onOff(injected["rea-allow"]),
		"WATERMARK-REMOVER "+onOff(injected[watermarkRemover]),
		"HEADROOM "+saverState(st.Headroom, st.Note != ""),
	)
	var named []string
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, ss.Skills...), ss.Reminded...) {
		if id == "rea-allow" || id == watermarkRemover || seen[id] || id == "" {
			continue
		}
		seen[id] = true
		named = append(named, logName(id))
	}
	if len(named) > 0 {
		parts = append(parts, "SKILLS "+strings.Join(named, " "))
	}
	return strings.Join(parts, " · ")
}

// rtkState names the compactor's outcome: the filters it shrank, the switch
// with nothing to shrink, or off. RTK has no routing mode, so it never says
// SKIPPED, and the names let a line be matched to its usage row.
func rtkState(st saver.Stats, skipped bool) string {
	switch {
	case len(st.Filters) > 0:
		return "RTK ON (" + strings.Join(st.Filters, ",") + ")"
	case skipped:
		return "RTK ON"
	}
	return "RTK OFF"
}

// saverState names one saver's outcome: it acted, it was on with nothing to do,
// or it was off.
func saverState(on, skipped bool) string {
	switch {
	case on:
		return "ON"
	case skipped:
		return "SKIPPED"
	}
	return "OFF"
}

// onOff is saverState for a switch-only entry, which has no skipped state.
func onOff(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}

// doneLine is the closing line of a request: timing, tokens and status.
func doneLine(r analytics.Record) string {
	var b strings.Builder
	b.WriteString("id=" + r.RequestID + " DONE " + strconv.FormatInt(r.LatencyMs, 10) + "ms")
	if r.TTFBMs > 0 {
		b.WriteString(" · TTFT " + strconv.FormatInt(r.TTFBMs, 10) + "ms")
	}
	b.WriteString(" · IN " + strconv.FormatInt(r.PromptTokens, 10))
	if r.CachedTokens > 0 {
		b.WriteString(" (CACHE ↻" + strconv.FormatInt(r.CachedTokens, 10) + ")")
	}
	b.WriteString(" · OUT " + strconv.FormatInt(r.CompletionTokens, 10))
	b.WriteString(" · " + strconv.Itoa(r.Status))
	return b.String()
}
