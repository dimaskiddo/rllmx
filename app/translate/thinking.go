package translate

import (
	"rllmx/app/routing"
	"rllmx/app/upstream"
)

// ModelThinking are the two thinking settings a registry model may carry, in
// the grammar of a name suffix.
type ModelThinking struct {
	// Default applies only when the request asks for nothing.
	Default string
	// Fixed is decided by the model id and beats everything else.
	Fixed string
}

// ResolveThinking decides what thinking a request gets. The order is the model's
// fixed setting, then the name suffix, then the client's own field, then the
// model's default. A model that does not reason gets none.
func ResolveThinking(suffix routing.Thinking, client Thinking, model ModelThinking, c upstream.Capability) Thinking {
	if !c.Reasoning {
		return Thinking{}
	}
	if t := fromSuffix(model.Fixed); t.Set() {
		return t
	}
	if suffix.Set() {
		return Thinking{Mode: suffix.Mode, Level: suffix.Level, Budget: suffix.Budget}
	}
	if client.Set() {
		return client
	}
	return fromSuffix(model.Default)
}

func fromSuffix(v string) Thinking {
	if v == "" {
		return Thinking{}
	}
	s := routing.ParseThinking(v)
	return Thinking{Mode: s.Mode, Level: s.Level, Budget: s.Budget}
}

// ClampLevel moves a requested level onto the nearest one the model declares,
// so a level it does not know is not sent. none is only honored when declared.
func ClampLevel(level string, declared []string, order []string) string {
	if len(declared) == 0 {
		return level
	}
	for _, d := range declared {
		if d == level {
			return level
		}
	}
	rank := func(l string) int {
		for i, o := range order {
			if o == l {
				return i
			}
		}
		return -1
	}
	want := rank(level)
	if want < 0 {
		return declared[0]
	}
	best, bestDist := declared[0], 1<<30
	for _, d := range declared {
		r := rank(d)
		if r < 0 {
			continue
		}
		dist := r - want
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			best, bestDist = d, dist
		}
	}
	return best
}

// LevelOrder is the order of thinking levels from least to most.
var LevelOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// DropThinkingForPrefill removes thinking from a request whose last message is
// not the user's: a model asked to continue its own turn cannot also start a
// thinking block.
func DropThinkingForPrefill(r *Request) {
	if n := len(r.Messages); n > 0 && r.Messages[n-1].Role == RoleAssistant {
		r.Thinking = Thinking{}
	}
}
