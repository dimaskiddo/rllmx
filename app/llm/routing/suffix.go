// Package routing turns the model string a client sent into a provider, a model
// or a combo.
package routing

import (
	"strconv"
	"strings"

	"rllmx/app/llm/upstream"
)

const (
	contextMarker = "[1m]"
	maxBudget     = 1_000_000
)

// Thinking modes a suffix can ask for.
const (
	ThinkingNone   = "none"
	ThinkingAuto   = "auto"
	ThinkingLevel  = "level"
	ThinkingBudget = "budget"
)

// Thinking is the override a `name(value)` suffix carries. The zero value means
// the suffix was absent or its value unknown.
type Thinking struct {
	Mode   string
	Level  string
	Budget int
}

// Set reports whether the suffix asked for anything.
func (t Thinking) Set() bool { return t.Mode != "" }

// splitSuffixes removes a trailing context marker and a trailing thinking
// suffix, in either order, and reports both. Doing it before anything else is
// what makes `x[1m](high)` and `x(high)[1m]` land in one place.
func splitSuffixes(name string) (string, bool, Thinking) {
	var (
		marker   bool
		thinking Thinking
		seenThk  bool
	)
	for {
		name = strings.TrimSpace(name)
		if n := len(name); n >= len(contextMarker) && strings.EqualFold(name[n-len(contextMarker):], contextMarker) {
			marker = true
			name = name[:n-len(contextMarker)]
			continue
		}
		if !seenThk && strings.HasSuffix(name, ")") {
			if open := strings.LastIndexByte(name, '('); open >= 0 {
				thinking = parseThinking(name[open+1 : len(name)-1])
				seenThk = true
				name = name[:open]
				continue
			}
		}
		return name, marker, thinking
	}
}

// ParseThinking reads the value of a thinking suffix, `none`, `auto`, a level or a
// token budget. An unknown value is the zero Thinking.
func ParseThinking(v string) Thinking { return parseThinking(v) }

func parseThinking(v string) Thinking {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case v == "none" || v == "off":
		return Thinking{Mode: ThinkingNone}
	case v == "auto":
		return Thinking{Mode: ThinkingAuto}
	case upstream.IsEffortLevel(v):
		return Thinking{Mode: ThinkingLevel, Level: v}
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= maxBudget && v == strconv.Itoa(n) {
		return Thinking{Mode: ThinkingBudget, Budget: n}
	}
	return Thinking{}
}
