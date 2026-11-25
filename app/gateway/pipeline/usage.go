package pipeline

import (
	"strconv"
	"strings"

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
	r.Savers, r.Skills = c.saverStats.Names(), c.skillStats.Names()
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
	}
	if name := c.row.ComboName; name != "" {
		b.WriteString(` · COMBO "` + logName(name) + `"`)
	}
	b.WriteString(" · ACC " + logName(c.row.ConnectionID))
	b.WriteString(" · try " + strconv.Itoa(c.attempt))
	return b.String()
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
		"RTK " + saverState(len(st.Filters) > 0, skipped["rtk"]),
		"CAVEMAN " + saverState(level("caveman"), skipped["caveman"]),
		"PONYTAIL " + saverState(level("ponytail"), skipped["ponytail"]),
	}
	if st.CustomChars > 0 {
		parts = append(parts, "CUSTOMPROMPT ON ("+strconv.Itoa(st.CustomChars)+"c)")
	}
	parts = append(parts,
		"ALLOW REA "+onOff(injected["rea-allow"]),
		"WATERMARK-REMOVER "+onOff(injected["watermarks-remover"]),
		"HEADROOM "+saverState(st.Headroom, st.Note != ""),
	)
	var named []string
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, ss.Skills...), ss.Reminded...) {
		if id == "rea-allow" || id == "watermarks-remover" || seen[id] || id == "" {
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
