package saver

import (
	"context"
	"strconv"
	"strings"

	"rllmx/app/gateway/saver/caveman"
	"rllmx/app/gateway/saver/headroom"
	"rllmx/app/gateway/saver/ponytail"
	"rllmx/app/gateway/saver/prompt"
	"rllmx/app/gateway/saver/rtk"
	"rllmx/app/llm/translate"
)

// Compressor is the Headroom client, owned by its own package.
type Compressor = headroom.Compressor

// Headroom is the sidecar's setting, owned by its own package.
type Headroom = headroom.Options

// Options is one request's saver configuration, read from the settings row.
type Options struct {
	RTK      bool
	Headroom Headroom
	Caveman  Prompt
	Ponytail Prompt
	Custom   Custom
}

// Prompt is one built-in prompt saver. Mode is "always" or "smart"; smart
// injects only when the latest user message is about the saver's topic.
type Prompt struct {
	Enabled bool
	Level   string
	Mode    string
}

// Custom is the operator's own system prompt.
type Custom struct {
	Enabled bool
	Text    string
}

// Stats is what one request's savers did, for the redacting log line.
type Stats struct {
	Saved    int
	Filters  []string
	Prompts  []string
	Headroom bool
	Note     string
}

// Changed reports whether anything was rewritten.
func (s Stats) Changed() bool {
	return s.Saved > 0 || len(s.Prompts) > 0 || s.Headroom || s.Note != ""
}

// Line renders the log line: counts and names only, never prompt text and never
// the sidecar's address.
func (s Stats) Line() string {
	var parts []string
	if len(s.Filters) > 0 {
		parts = append(parts, "rtk="+strings.Join(s.Filters, "+")+" saved="+strconv.Itoa(s.Saved)+"B")
	}
	if s.Headroom {
		parts = append(parts, "headroom=on")
	}
	if s.Note != "" {
		parts = append(parts, "headroom="+s.Note)
	}
	if len(s.Prompts) > 0 {
		parts = append(parts, "prompts="+strings.Join(s.Prompts, "+"))
	}
	return strings.Join(parts, " ")
}

// The two prompt topics, each package's data paired with the shared scorer. They
// are scored together, because the reference weighs a term by how many topics
// use it.
var (
	cavemanTopic  = prompt.Topic{Name: "caveman", Description: caveman.Description, Triggers: caveman.Triggers, Keywords: caveman.Keywords, Threshold: caveman.Threshold}
	ponytailTopic = prompt.Topic{Name: "ponytail", Description: ponytail.Description, Triggers: ponytail.Triggers, Keywords: ponytail.Keywords, Threshold: ponytail.Threshold}
)

// saverTopics are the topics a smart mode scores against.
var saverTopics = []prompt.Topic{cavemanTopic, ponytailTopic}

// Apply runs every enabled saver on the hub request in the reference's order:
// tool output, the sidecar, then the prompts. It never returns an error: a step
// that cannot run leaves the request as it was.
func Apply(ctx context.Context, comp *Compressor, r *translate.Request, o Options) Stats {
	var st Stats
	if r == nil {
		return st
	}
	if o.RTK {
		applyRTK(r, &st)
	}
	if o.Headroom.Enabled && comp != nil {
		res := comp.Apply(ctx, r, o.Headroom)
		st.Headroom = res.Compressed
		st.Note = res.Note
	}
	query := ""
	if o.Caveman.Mode == "smart" || o.Ponytail.Mode == "smart" {
		query = prompt.LatestUser(r)
	}
	applyPrompt(r, o.Caveman, caveman.Level, cavemanTopic, query, "caveman", &st)
	applyPrompt(r, o.Ponytail, ponytail.Level, ponytailTopic, query, "ponytail", &st)
	if o.Custom.Enabled {
		if prompt.Inject(r, strings.TrimSpace(o.Custom.Text)) {
			st.Prompts = append(st.Prompts, "custom")
		}
	}
	return st
}

// applyRTK compacts the text of every successful tool result in place.
func applyRTK(r *translate.Request, st *Stats) {
	seen := map[string]bool{}
	for i := range r.Messages {
		m := &r.Messages[i]
		if m.Role != translate.RoleTool || m.Error {
			continue
		}
		for j := range m.Content {
			part := &m.Content[j]
			if part.Type != translate.PartText {
				continue
			}
			out, name := rtk.Compress(part.Text)
			if name == "" {
				continue
			}
			st.Saved += len(part.Text) - len(out)
			part.Text = out
			if !seen[name] {
				seen[name] = true
				st.Filters = append(st.Filters, name)
			}
		}
	}
}

// applyPrompt injects one configured prompt, honouring its activation mode. The
// scorer weighs a term by how many topics use it, so the peers travel with it.
func applyPrompt(r *translate.Request, p Prompt, level func(string) string, t prompt.Topic, query, label string, st *Stats) {
	text := level(p.Level)
	if !p.Enabled || text == "" {
		return
	}
	if p.Mode == "smart" && !t.Match(query, saverTopics...) {
		return
	}
	if prompt.Inject(r, text) {
		st.Prompts = append(st.Prompts, label)
	}
}
