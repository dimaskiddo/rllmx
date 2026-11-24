package combo

import (
	"github.com/tidwall/gjson"

	"rllmx/app/llm/translate"
	"rllmx/app/llm/upstream"
	"rllmx/app/store/document"
)

// Pool is one capability's fallback models: the models tried in front of a
// request whose own model cannot read what the turn carries.
type Pool struct {
	Enabled    bool
	RoundRobin bool
	Models     []string
}

// AdapterDefault is the model an enabled pool with no models falls back to, as
// the reference does.
const AdapterDefault = "opencode-zen/mimo-v2.6-flash-free"

// AdaptersFor reads the stored capacityAdapter row. A missing row, or a kind it
// does not name, reads as off; a name the row cannot use is dropped here rather
// than at request time.
func AdaptersFor(doc document.Doc) map[string]Pool {
	raw := gjson.Get(doc.String(), "capacityAdapter")
	if !raw.IsObject() {
		return nil
	}
	out := make(map[string]Pool, len(raw.Map()))
	for kind, entry := range raw.Map() {
		if !validKind(kind) || !entry.Get("enabled").Bool() {
			continue
		}
		pool := Pool{Enabled: true, RoundRobin: entry.Get("roundRobin").Bool()}
		for _, id := range entry.Get("models").Array() {
			if id.Type == gjson.String && id.String() != "" {
				pool.Models = append(pool.Models, id.String())
			}
		}
		if len(pool.Models) == 0 {
			pool.Models = []string{AdapterDefault}
		}
		out[kind] = pool
	}
	return out
}

// AdapterKinds are the pools a turn's needs call for, in the row's own order.
func AdapterKinds(n translate.Needs) []string {
	var out []string
	if n.Vision {
		out = append(out, "vision")
	}
	if n.PDF {
		out = append(out, "pdf")
	}
	if n.Audio {
		out = append(out, "audioInput")
	}
	if n.Video {
		out = append(out, "videoInput")
	}
	return out
}

// KindMet reports whether a capability row covers one adapter kind.
func KindMet(kind string, c upstream.Capability) bool {
	switch kind {
	case "vision":
		return c.Vision
	case "pdf":
		return c.PDF
	case "audioInput":
		return c.AudioInput
	case "videoInput":
		return c.VideoInput
	}
	return false
}

func validKind(kind string) bool {
	switch kind {
	case "vision", "pdf", "audioInput", "videoInput":
		return true
	}
	return false
}
