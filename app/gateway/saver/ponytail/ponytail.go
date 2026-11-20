// Package ponytail holds the minimal-code instruction and the topic it is
// routed by. The saver wires it in and decides when it runs. The level texts
// are the reference's own (the ponytail skill, MIT, github.com/DietrichGebert/ponytail),
// kept verbatim as the maintainer directed; an edit tracks the reference.
package ponytail

import "strings"

const (
	sharedPersona = "You are a lazy senior developer. Lazy means efficient, not careless. The best code is the code never written."

	sharedLadder = "Before writing code, stop at the first rung that holds: 1) Does this need to exist at all? (YAGNI) 2) Stdlib does it? Use it. 3) Native platform feature covers it? Use it (CSS over JS, DB constraint over app code). 4) Already-installed dependency solves it? Use it; never add a new one for what a few lines can do. 5) Can it be one line? One line. 6) Only then: the minimum code that works."

	sharedRules = "No unrequested abstractions (no interface with one implementation, no factory for one product, no config for a value that never changes). No boilerplate or scaffolding \"for later\". Deletion over addition. Boring over clever. Fewest files possible; shortest working diff wins. Two stdlib options the same size: take the edge-case-correct one. Mark deliberate simplifications with a `ponytail:` comment naming the ceiling and upgrade path."

	sharedOutput = "Code first. Then at most three short lines: what was skipped, when to add it. No essays or design notes. Pattern: `[code] → skipped: [X], add when [Y].`"

	sharedNotLazy = "Never simplify away: input validation at trust boundaries, error handling that prevents data loss, security, accessibility, anything explicitly requested. Non-trivial logic leaves ONE runnable check behind (an assert-based self-check or one small test file; no frameworks). Trivial one-liners need no test."

	sharedPersistence = "ACTIVE EVERY RESPONSE. No drift back to over-building. Still active if unsure."
)

// shared is every clause the reference carries after the level's own line.
var shared = []string{sharedLadder, sharedRules, sharedOutput, sharedNotLazy, sharedPersistence}

func level(own string) string {
	return strings.Join(append([]string{sharedPersona, own}, shared...), " ")
}

// levels bias the model toward minimal code.
var levels = map[string]string{
	"lite":  level("Lite: build what's asked, but name the lazier alternative in one line. User picks."),
	"full":  level("Full: the ladder enforced. Stdlib and native first. Shortest diff, shortest explanation."),
	"ultra": level("Ultra: YAGNI extremist. Deletion before addition. Ship the one-liner and challenge the rest of the requirement in the same response."),
}

// Level is the instruction for a level, or "" for a name this package does not
// know.
func Level(name string) string { return levels[name] }

// The routing table the smart activation mode scores. It is data only: the
// saver pairs it with the shared scorer, so this package needs no import.
var (
	Description = "minimal code, reuse what exists, delete before adding"
	Triggers    = []string{
		"refactor", "simplify", "minimal code", "yagni", "delete unused", "reduce complexity",
		"clean up", "remove dead code", "over-engineered", "too much boilerplate", "keep it simple",
	}
	Keywords = []string{
		"refactor", "simplify", "minimal", "yagni", "delete", "remove", "clean",
		"complexity", "boilerplate", "stdlib", "native", "lean", "trim",
	}
	Threshold = 0.30
)
